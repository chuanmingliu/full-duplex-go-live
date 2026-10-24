// Package quotesvc is a vehicle quotation service with the business's own
// interface (the document of 2026-07-21): an asynchronous inquiry that
// answers "ok" at once and prices in the background, and a query that
// returns the latest quote for a phone and a plate. It runs on the test box
// so the whole voice path — the default quotation prefetched while the
// phone rings, a second quotation with its prehook while the service works,
// the posthook and the readout — is exercised against a service that takes
// real seconds, before the business's intranet service is reachable; when
// it is, the lead service's quote-api base_url moves and nothing else does.
//
// Pricing is the placeholder table (rating.json: the owner's word, 10-24),
// over a vehicle table (vehicles.json) seeded on the box, never in the
// repository. Every inquiry and quote is appended to quotes.jsonl and the
// latest quote per phone+plate is rebuilt from it at start.
package quotesvc

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Vehicle is what the service knows about a plate.
type Vehicle struct {
	Plate string  `json:"plate"`
	Price float64 `json:"price"` // purchase price, yuan
	Age   int     `json:"age"`   // years
	Seats int     `json:"seats"`
	NCD   int     `json:"ncd"` // claims last year
	Phone string  `json:"phone,omitempty"`
}

// Rating is the placeholder table, by the document's coverage codes.
type Rating struct {
	Coverages map[string]CoverageRating `json:"coverages"`
	// AgeFactors and NCDFactors multiply every business line.
	AgeFactors []Band             `json:"age_factors"`
	NCDFactors map[string]float64 `json:"ncd_factors"`
	// Compulsory by seat band, and the vehicle tax by seat band.
	Compulsory []Band `json:"compulsory_by_seats"`
	Tax        []Band `json:"tax_by_seats"`
}

// CoverageRating prices one code: a rate on the vehicle's price with a
// floor, a table on the sum insured (yuan → premium; the next table step
// at or above the sum is used), or per 万 of the sum insured per seat.
type CoverageRating struct {
	Name       string             `json:"name"`
	Rate       float64            `json:"rate,omitempty"`
	Min        float64            `json:"min,omitempty"`
	Table      map[string]float64 `json:"table,omitempty"`
	PerWan     float64            `json:"per_wan,omitempty"`
	PerSeat    bool               `json:"per_seat,omitempty"`
	Passengers bool               `json:"passengers,omitempty"` // seats − 1
}

// Band is max → value.
type Band struct {
	Max float64 `json:"max"`
	V   float64 `json:"v"`
}

// DefaultRating is the placeholder table, the same figures as the lead
// service's quote-rules where they correspond.
const DefaultRating = `{
  "coverages": {
    "951": {"name": "车损险", "rate": 0.012, "min": 600},
    "952": {"name": "三者险", "table": {"1000000": 1150, "2000000": 1450, "3000000": 1700, "5000000": 2100}},
    "953": {"name": "司机险", "per_wan": 9},
    "954": {"name": "乘客险", "per_wan": 9, "passengers": true},
    "955": {"name": "划痕险", "table": {"2000": 320, "5000": 560}},
    "956": {"name": "玻璃险", "rate": 0.0015, "min": 120},
    "957": {"name": "医保外用药", "table": {"10000": 80, "20000": 120}}
  },
  "age_factors": [{"max": 1, "v": 1.0}, {"max": 3, "v": 0.95}, {"max": 6, "v": 0.9}, {"max": 99, "v": 0.85}],
  "ncd_factors": {"0": 1.0, "1": 0.85, "2": 0.7, "3": 0.6},
  "compulsory_by_seats": [{"max": 5, "v": 950}, {"max": 9, "v": 1100}, {"max": 99, "v": 1400}],
  "tax_by_seats": [{"max": 5, "v": 420}, {"max": 9, "v": 480}, {"max": 99, "v": 600}]
}`

// Inquiry is the document's request.
type Inquiry struct {
	Phone                     string     `json:"phone"`
	VehicleNo                 string     `json:"vehicleNo"`
	BizOrigin                 string     `json:"bizOrigin,omitempty"`
	BizSource                 string     `json:"bizSource,omitempty"`
	IsInsureBusinessInsurance bool       `json:"isInsureBusinessInsurance"`
	IsInsureCompelInsurance   bool       `json:"isInsureCompelInsurance"`
	CoverageList              []Coverage `json:"coverageList,omitempty"`
}

// Coverage is coverageList[].
type Coverage struct {
	CoverageCode    string `json:"coverageCode"`
	CoverageName    string `json:"coverageName"`
	CoverageType    string `json:"coverageType"`
	IsNonDeductible string `json:"isNonDeductible"`
	SumInsured      string `json:"sumInsured"`
}

// QuoteInfo is quotePriceInfos[] of the query's answer.
type QuoteInfo struct {
	QuotePriceID        int64        `json:"quotePriceId"`
	Result              string       `json:"result"`
	ResultMessage       string       `json:"resultMessage,omitempty"`
	PlanType            string       `json:"planType"`
	InsureFlowCode      string       `json:"insureFlowCode"`
	SumPremium          string       `json:"sumPreimum"`
	SelfPricingRatio    string       `json:"selfPricingRatio"`
	BusinessQuoteResult string       `json:"businessQuoteResult"`
	BusinessSumPremium  string       `json:"businessSumPreimum"`
	BizStandardTotal    string       `json:"bizStandardTotalPremium"`
	BizDiscountPremium  string       `json:"bizDiscountPremium"`
	BusinessDiscount    string       `json:"businessDiscount"`
	BusinessEffective   string       `json:"businessEffectiveDate"`
	BusinessExpire      string       `json:"businessExpireDate"`
	CompelQuoteResult   string       `json:"compelQuoteResult"`
	CompelSumPremium    string       `json:"compelSumPreimum"`
	CompelEffective     string       `json:"compelEffectiveDate"`
	CompelExpire        string       `json:"compelExpireDate"`
	TaxPremium          string       `json:"taxPreimum"`
	IsEffective         bool         `json:"isEffective"`
	OrderTime           string       `json:"orderTime"`
	OrderRemainTime     int64        `json:"orderRemainTime"`
	CoverageList        []LinePriced `json:"coverageList"`
}

// LinePriced is coverageList[] of a quote.
type LinePriced struct {
	CoverageCode       string `json:"coverageCode"`
	CoverageType       string `json:"coverageType"`
	CoverageSimpleName string `json:"coverageSimpleName"`
	Amount             string `json:"amount"`
	Tag                string `json:"tag"`
	CoveragePremium    string `json:"coveragePreimum"`
	StandardPremium    string `json:"standardPremium"`
}

// record is one line of quotes.jsonl.
type record struct {
	At      time.Time  `json:"at"`
	Kind    string     `json:"kind"` // inquiry | quote
	Key     string     `json:"key"`  // phone|plate
	Inquiry *Inquiry   `json:"inquiry,omitempty"`
	Quote   *QuoteInfo `json:"quote,omitempty"`
	TookMS  int64      `json:"took_ms,omitempty"`
}

// Options configure a service.
type Options struct {
	Dir       string
	ComputeMS int // the pricing's time, before jitter
	JitterMS  int
	Workers   int
	Log       *slog.Logger
	Now       func() time.Time
	Sleep     func(time.Duration) // tests
}

// Service is the running service.
type Service struct {
	opts   Options
	log    *slog.Logger
	rating Rating

	mu       sync.Mutex
	vehicles map[string]Vehicle
	latest   map[string]*QuoteInfo // key → latest quote (or a pending record)
	pending  map[string]bool
	seq      int64
	logf     *os.File
	jobs     chan Inquiry
	token    string
}

// Open loads the data directory (creating it and the seeds) and starts the
// workers.
func Open(opts Options) (*Service, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = time.Sleep
	}
	if opts.Workers <= 0 {
		opts.Workers = 2
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, err
	}
	s := &Service{opts: opts, log: opts.Log, vehicles: map[string]Vehicle{}, latest: map[string]*QuoteInfo{}, pending: map[string]bool{}, jobs: make(chan Inquiry, 64)}
	rp := filepath.Join(opts.Dir, "rating.json")
	raw, err := os.ReadFile(rp)
	if errors.Is(err, os.ErrNotExist) {
		raw = []byte(DefaultRating)
		_ = os.WriteFile(rp, raw, 0o600)
	} else if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.rating); err != nil {
		return nil, fmt.Errorf("rating.json: %w", err)
	}
	if err := s.loadVehicles(); err != nil {
		return nil, err
	}
	if err := s.replay(); err != nil {
		return nil, err
	}
	tp := filepath.Join(opts.Dir, "api.token")
	if t, err := os.ReadFile(tp); err == nil {
		s.token = strings.TrimSpace(string(t))
	} else {
		b := make([]byte, 24)
		_, _ = rand.Read(b)
		s.token = hex.EncodeToString(b)
		_ = os.WriteFile(tp, []byte(s.token+"\n"), 0o600)
	}
	for i := 0; i < opts.Workers; i++ {
		go s.worker()
	}
	return s, nil
}

func (s *Service) loadVehicles() error {
	raw, err := os.ReadFile(filepath.Join(s.opts.Dir, "vehicles.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []Vehicle
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("vehicles.json: %w", err)
	}
	for _, v := range list {
		s.vehicles[normPlate(v.Plate)] = v
	}
	return nil
}

// AddVehicle adds or replaces a vehicle and writes the table.
func (s *Service) AddVehicle(v Vehicle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vehicles[normPlate(v.Plate)] = v
	list := make([]Vehicle, 0, len(s.vehicles))
	for _, x := range s.vehicles {
		list = append(list, x)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Plate < list[j].Plate })
	raw, _ := json.MarshalIndent(list, "", " ")
	return os.WriteFile(filepath.Join(s.opts.Dir, "vehicles.json"), raw, 0o600)
}

// Vehicles is how many plates are known.
func (s *Service) Vehicles() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.vehicles)
}

func normPlate(p string) string { return strings.ToUpper(strings.TrimSpace(p)) }
func key(phone, plate string) string {
	return strings.TrimSpace(phone) + "|" + normPlate(plate)
}

func (s *Service) replay() error {
	path := filepath.Join(s.opts.Dir, "quotes.jsonl")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	s.logf = f
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r record
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		if r.Kind == "quote" && r.Quote != nil {
			s.latest[r.Key] = r.Quote
			if r.Quote.QuotePriceID > s.seq {
				s.seq = r.Quote.QuotePriceID
			}
		}
	}
	return nil
}

func (s *Service) append(r record) {
	r.At = s.opts.Now()
	b, _ := json.Marshal(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logf != nil {
		_, _ = s.logf.Write(append(b, '\n'))
	}
}

// Submit validates an inquiry and queues it. It is the document's
// vehicleApplyInquiryPriceAsync: "ok" now, the quote later.
func (s *Service) Submit(in Inquiry) error {
	if strings.TrimSpace(in.Phone) == "" {
		return errors.New("phone 必填")
	}
	if strings.TrimSpace(in.VehicleNo) == "" {
		return errors.New("vehicleNo 必填")
	}
	if !in.IsInsureCompelInsurance && len(in.CoverageList) == 0 {
		return errors.New("coverageList 不能为空")
	}
	k := key(in.Phone, in.VehicleNo)
	s.mu.Lock()
	s.pending[k] = true
	s.mu.Unlock()
	s.append(record{Kind: "inquiry", Key: k, Inquiry: &in})
	select {
	case s.jobs <- in:
	default:
		return errors.New("系统繁忙，请稍后再试")
	}
	return nil
}

func (s *Service) worker() {
	for in := range s.jobs {
		began := s.opts.Now()
		wait := time.Duration(s.opts.ComputeMS) * time.Millisecond
		if s.opts.JitterMS > 0 {
			wait += time.Duration(int64(s.opts.JitterMS)*(s.opts.Now().UnixNano()%1000)/1000) * time.Millisecond
		}
		if wait > 0 {
			s.opts.Sleep(wait)
		}
		q := s.price(in)
		k := key(in.Phone, in.VehicleNo)
		s.mu.Lock()
		s.latest[k] = q
		delete(s.pending, k)
		s.mu.Unlock()
		s.append(record{Kind: "quote", Key: k, Quote: q, TookMS: s.opts.Now().Sub(began).Milliseconds()})
		s.log.Info("quotesvc: priced", "id", q.QuotePriceID, "result", q.Result, "total", q.SumPremium, "lines", len(q.CoverageList), "ms", s.opts.Now().Sub(began).Milliseconds())
	}
}

// Latest is the document's queryLastQuoteDetailByPhone: the newest quote
// for the phone and plate, or nil when there has never been one.
func (s *Service) Latest(phone, plate string) *QuoteInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest[key(phone, plate)]
}

func band(bands []Band, x float64) float64 {
	for _, b := range bands {
		if x <= b.Max {
			return b.V
		}
	}
	if len(bands) > 0 {
		return bands[len(bands)-1].V
	}
	return 0
}

func money(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', 2, 64) }

// price is the placeholder rating.
func (s *Service) price(in Inquiry) *QuoteInfo {
	s.mu.Lock()
	s.seq++
	id := s.seq
	v, known := s.vehicles[normPlate(in.VehicleNo)]
	s.mu.Unlock()
	now := s.opts.Now()
	q := &QuoteInfo{QuotePriceID: id, InsureFlowCode: fmt.Sprintf("QS%06d", id), PlanType: "1", OrderTime: now.Format("2006-01-02 15:04:05"), OrderRemainTime: 3600}
	if !known {
		q.Result, q.ResultMessage = "1", "车辆信息有误：未找到该车牌"
		return q
	}
	factor := band(s.rating.AgeFactors, float64(v.Age))
	if f, ok := s.rating.NCDFactors[strconv.Itoa(v.NCD)]; ok {
		factor *= f
	}
	var standard, business float64
	for _, c := range in.CoverageList {
		r, ok := s.rating.Coverages[c.CoverageCode]
		if !ok {
			continue
		}
		sum, _ := strconv.ParseFloat(strings.TrimSpace(c.SumInsured), 64)
		if c.SumInsured == "1" || sum == 1 {
			sum = v.Price
		}
		var base float64
		switch {
		case r.Rate > 0:
			base = math.Max(sum*r.Rate, r.Min)
		case len(r.Table) > 0:
			keys := make([]float64, 0, len(r.Table))
			for k := range r.Table {
				f, _ := strconv.ParseFloat(k, 64)
				keys = append(keys, f)
			}
			sort.Float64s(keys)
			pick := keys[len(keys)-1]
			for _, k := range keys {
				if sum <= k {
					pick = k
					break
				}
			}
			base = r.Table[strconv.FormatFloat(pick, 'f', -1, 64)]
			sum = pick
		case r.PerWan > 0:
			n := 1.0
			if r.Passengers {
				n = math.Max(float64(v.Seats-1), 1)
			}
			base = sum / 10000 * r.PerWan * n
		}
		premium := base * factor
		tag := ""
		if sum >= 10000 {
			tag = strconv.FormatFloat(sum/10000, 'f', -1, 64) + "万"
		}
		q.CoverageList = append(q.CoverageList, LinePriced{CoverageCode: c.CoverageCode, CoverageType: c.CoverageType, CoverageSimpleName: r.Name,
			Amount: strconv.FormatFloat(sum, 'f', -1, 64), Tag: tag, CoveragePremium: money(premium), StandardPremium: money(base)})
		standard += base
		business += premium
	}
	total := 0.0
	if len(q.CoverageList) > 0 {
		q.BusinessQuoteResult = "0"
		q.BizStandardTotal, q.BusinessSumPremium = money(standard), money(business)
		q.BizDiscountPremium, q.BusinessDiscount, q.SelfPricingRatio = money(standard-business), money(factor), money(factor)
		q.BusinessEffective, q.BusinessExpire = now.AddDate(0, 0, 1).Format("2006-01-02"), now.AddDate(1, 0, 0).Format("2006-01-02")
		total += business
	}
	if in.IsInsureCompelInsurance {
		compel := band(s.rating.Compulsory, float64(v.Seats))
		tax := band(s.rating.Tax, float64(v.Seats))
		q.CompelQuoteResult, q.CompelSumPremium, q.TaxPremium = "0", money(compel), money(tax)
		q.CompelEffective, q.CompelExpire = now.AddDate(0, 0, 1).Format("2006-01-02"), now.AddDate(1, 0, 0).Format("2006-01-02")
		total += compel + tax
	}
	q.Result, q.SumPremium, q.IsEffective = "0", money(total), true
	return q
}

// --- HTTP -------------------------------------------------------------------

// Handler serves the document's two endpoints under /intranet, the health
// check, and an operator's view of the latest quotes (token).
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		n, p := len(s.latest), len(s.pending)
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"status": "ok", "service": "quotesvc", "vehicles": s.Vehicles(), "quotes": n, "pending": p})
	})
	mux.HandleFunc("POST /intranet/vehicle/innerProcess/vehicleApplyInquiryPriceAsync", func(w http.ResponseWriter, r *http.Request) {
		var in Inquiry
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			writeJSON(w, 200, map[string]any{"code": 1, "msg": "请求体不是合法的 JSON"})
			return
		}
		if err := s.Submit(in); err != nil {
			writeJSON(w, 200, map[string]any{"code": 1, "msg": err.Error()})
			return
		}
		s.log.Info("quotesvc: inquiry", "coverages", len(in.CoverageList), "compel", in.IsInsureCompelInsurance, "source", in.BizSource)
		writeJSON(w, 200, map[string]any{"code": 0, "msg": "ok", "data": "ok"})
	})
	mux.HandleFunc("POST /intranet/vehicle/simpleProcess/queryLastQuoteDetailByPhone", func(w http.ResponseWriter, r *http.Request) {
		phone, plate := r.URL.Query().Get("phone"), r.URL.Query().Get("vehicleNo")
		if phone == "" || plate == "" {
			writeJSON(w, 200, map[string]any{"code": 1, "msg": "phone、vehicleNo 必填"})
			return
		}
		q := s.Latest(phone, plate)
		infos := []QuoteInfo{}
		if q != nil {
			infos = append(infos, *q)
		}
		writeJSON(w, 200, map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"Result": "0", "ResultMessage": "操作成功", "quotePriceInfos": infos}})
	})
	mux.HandleFunc("GET /api/quotes", func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && r.Header.Get("Authorization") != "Bearer "+s.token {
			writeJSON(w, 401, map[string]any{"error": "token"})
			return
		}
		s.mu.Lock()
		out := make([]map[string]any, 0, len(s.latest))
		for k, q := range s.latest {
			// Numbers and ids only: the key holds a phone and a plate.
			out = append(out, map[string]any{"key_len": len(k), "id": q.QuotePriceID, "result": q.Result, "total": q.SumPremium, "lines": len(q.CoverageList), "at": q.OrderTime})
		}
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"quotes": out})
	})
	return mux
}
