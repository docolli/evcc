package tariff

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/tariff/solcast"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/request"
	"github.com/evcc-io/evcc/util/transport"
)

type Solcast struct {
	*request.Helper
	mux     sync.Mutex
	log     *util.Logger
	siteID  string
	data    api.Rates
	updated time.Time
}

var _ api.Tariff = (*Solcast)(nil)

func init() {
	registry.Add("solcast", NewSolcastFromConfig)
}

func NewSolcastFromConfig(other map[string]interface{}) (api.Tariff, error) {
	var cc struct {
		SiteID string
		Token  string
	}

	if err := util.DecodeOther(other, &cc); err != nil {
		return nil, err
	}

	if cc.SiteID == "" {
		return nil, errors.New("missing site id")
	}

	if cc.Token == "" {
		return nil, errors.New("missing token")
	}

	log := util.NewLogger("solcast").Redact(cc.SiteID, cc.Token)

	t := &Solcast{
		log:    log,
		siteID: cc.SiteID,
		Helper: request.NewHelper(log),
	}

	t.Client.Transport = transport.BearerAuth(cc.Token, t.Client.Transport)

	done := make(chan error)
	go t.run(done)
	err := <-done

	return t, err
}

func (t *Solcast) run(done chan error) {
	var once sync.Once
	bo := newBackoff()

	uri := fmt.Sprintf("https://api.solcast.com.au/rooftop_sites/%s/forecasts?format=json", t.siteID)

	for ; true; <-time.Tick(time.Hour) {
		var res solcast.Forecasts

		if err := backoff.Retry(func() error {
			return t.GetJSON(uri, &res)
		}, bo); err != nil {
			once.Do(func() { done <- err })

			t.log.ERROR.Println(err)
			continue
		}

		once.Do(func() { close(done) })

		t.mux.Lock()
		t.updated = time.Now()

		t.data = make(api.Rates, 0, len(res.Forecasts))

		for _, r := range res.Forecasts {
			rate := api.Rate{
				Start: r.PeriodEnd.Add(-r.Period.Duration()).Local(),
				End:   r.PeriodEnd.Local(),
				Price: r.PvEstimate,
			}

			t.data = append(t.data, rate)
		}

		t.mux.Unlock()
	}
}

// Rates implements the api.Tariff interface
func (t *Solcast) Rates() (api.Rates, error) {
	t.mux.Lock()
	defer t.mux.Unlock()
	return slices.Clone(t.data), outdatedError(t.updated, time.Hour)
}

// Type implements the api.Tariff interface
func (t *Solcast) Type() api.TariffType {
	return api.TariffTypeGeneration
}