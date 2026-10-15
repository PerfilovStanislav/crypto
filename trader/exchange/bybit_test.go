package exchange

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"trader/config"
)

const (
	testKey    = "test-key"
	testSecret = "test-secret"
)

// newTestClient points a client at an httptest server; no request leaves the
// machine.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(config.BybitConfig{APIKey: testKey, APISecret: testSecret, BaseURL: srv.URL})
}

func ok(result string) string {
	return `{"retCode":0,"retMsg":"OK","result":` + result + `,"retExtInfo":{},"time":1}`
}

// checkSigned verifies Bybit v5 auth headers for the given signed payload
// (query string for GET, body for POST).
func checkSigned(t *testing.T, r *http.Request, payload string) {
	t.Helper()
	if got := r.Header.Get("X-BAPI-API-KEY"); got != testKey {
		t.Errorf("api key header = %q", got)
	}
	ts := r.Header.Get("X-BAPI-TIMESTAMP")
	recv := r.Header.Get("X-BAPI-RECV-WINDOW")
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(ts + testKey + recv + payload))
	if want := hex.EncodeToString(mac.Sum(nil)); r.Header.Get("X-BAPI-SIGN") != want {
		t.Errorf("signature mismatch for payload %q", payload)
	}
}

func decodeBody(t *testing.T, r *http.Request) (string, map[string]any) {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("body is not JSON: %s", raw)
	}
	return string(raw), m
}

func TestMarketBuyByQuoteRequest(t *testing.T) {
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v5/order/create" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content type = %q", ct)
		}
		var raw string
		raw, body = decodeBody(t, r)
		checkSigned(t, r, raw)
		io.WriteString(w, ok(`{"orderId":"1001","orderLinkId":"tb-SOLUSDT-1-buy"}`))
	})

	id, err := c.PlaceOrder(MarketBuyByQuote("SOLUSDT", "50.000000", "tb-SOLUSDT-1-buy"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "1001" {
		t.Errorf("order id = %q", id)
	}

	want := map[string]any{
		"category":    "spot",
		"symbol":      "SOLUSDT",
		"side":        "Buy",
		"orderType":   "Market",
		"qty":         "50.000000", // USDT, not SOL
		"marketUnit":  "quoteCoin",
		"orderFilter": "Order",
		"orderLinkId": "tb-SOLUSDT-1-buy",
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %v\nwant  %v", body, want)
	}
}

func TestStopMarketSellRequest(t *testing.T) {
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var raw string
		raw, body = decodeBody(t, r)
		checkSigned(t, r, raw)
		io.WriteString(w, ok(`{"orderId":"1002"}`))
	})

	if _, err := c.PlaceOrder(StopMarketSell("SOLUSDT", "0.3327", "135.12", "tb-SOLUSDT-1-sl")); err != nil {
		t.Fatal(err)
	}

	// No takeProfit/stopLoss (unsupported on spot market orders) and no
	// marketUnit (unsupported on conditional orders): qty is in SOL.
	want := map[string]any{
		"category":     "spot",
		"symbol":       "SOLUSDT",
		"side":         "Sell",
		"orderType":    "Market",
		"qty":          "0.3327",
		"triggerPrice": "135.12",
		"orderFilter":  "StopOrder",
		"orderLinkId":  "tb-SOLUSDT-1-sl",
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %v\nwant  %v", body, want)
	}
}

func TestRetCodeIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"retCode":170131,"retMsg":"Insufficient balance.","result":{}}`)
	})

	_, err := c.PlaceOrder(MarketBuyByQuote("SOLUSDT", "50", "x"))
	if err == nil || !strings.Contains(err.Error(), "170131") {
		t.Fatalf("err = %v, want retCode 170131", err)
	}
}

func TestHTTPErrorIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})

	if _, err := c.GetCoinBalance("USDT"); err == nil {
		t.Fatal("want error")
	}
}

func TestGetKlinesStoresConfigTimeframe(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/market/kline" {
			t.Errorf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("interval") != "240" || q.Get("category") != "spot" || q.Get("symbol") != "SOLUSDT" || q.Get("limit") != "1000" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if r.Header.Get("X-BAPI-SIGN") != "" {
			t.Error("public endpoint must not be signed")
		}
		// newest first, like Bybit
		io.WriteString(w, ok(`{"category":"spot","symbol":"SOLUSDT","list":[
			["1792094400000","106.01","106.1","105.87","105.94","1275.17","135195.1"],
			["1792080000000","106.15","106.51","105.12","106.01","46034.77","4871482.1"]]}`))
	})

	candles, err := c.GetKlines("SOLUSDT", "4h", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 2 {
		t.Fatalf("got %d candles", len(candles))
	}

	first := candles[0]
	if first.Timeframe != "4h" {
		t.Errorf("Timeframe = %q, want the config value for the ClickHouse enum", first.Timeframe)
	}
	if !first.Timestamp.Equal(time.UnixMilli(1792080000000)) || first.Timestamp.Location() != time.UTC {
		t.Errorf("first candle = %v, want oldest first in UTC", first.Timestamp)
	}
	if first.Open != 106.15 || first.High != 106.51 || first.Low != 105.12 || first.Close != 106.01 || first.Volume != 46034.77 {
		t.Errorf("first candle OHLCV = %+v", first)
	}
}

func TestGetKlinesRejectsUnknownTimeframe(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected")
	})
	if _, err := c.GetKlines("SOLUSDT", "240", 10); err == nil {
		t.Fatal("want error for a Bybit interval passed as timeframe")
	}
}

func TestGetInstrument(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/market/instruments-info" || r.URL.Query().Get("symbol") != "SOLUSDT" {
			t.Errorf("got %s", r.URL)
		}
		// real SOLUSDT values
		io.WriteString(w, ok(`{"category":"spot","list":[{"symbol":"SOLUSDT","baseCoin":"SOL","quoteCoin":"USDT",
			"lotSizeFilter":{"basePrecision":"0.0001","quotePrecision":"0.000001","minOrderQty":"0.0001","minOrderAmt":"5"},
			"priceFilter":{"tickSize":"0.01"}}]}`))
	})

	inst, err := c.GetInstrument("SOLUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if inst.BaseCoin != "SOL" || inst.QuoteCoin != "USDT" || inst.MinOrderQty != 0.0001 || inst.MinOrderAmt != 5 {
		t.Errorf("instrument = %+v", inst)
	}

	checks := []struct{ got, want string }{
		{inst.FormatPrice(170.123), "170.12"},
		{inst.FormatPrice(135.125001), "135.13"},
		{inst.FormatPrice(170), "170.00"},
		{inst.FormatBaseQty(0.49995), "0.4999"}, // floor, never more than held
		{inst.FormatBaseQty(0.4995), "0.4995"},  // 0.4995/0.0001 is 4994.999...
		{inst.FormatQuoteQty(12.3456789), "12.345678"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("got %s, want %s", c.got, c.want)
		}
	}
}

func TestGetCoinBalance(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/account/wallet-balance" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.RawQuery != "accountType=UNIFIED&coin=SOL" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		checkSigned(t, r, r.URL.RawQuery)
		io.WriteString(w, ok(`{"list":[{"accountType":"UNIFIED","coin":[{"coin":"SOL","walletBalance":"0.4995","locked":"0"}]}]}`))
	})

	got, err := c.GetCoinBalance("SOL")
	if err != nil {
		t.Fatal(err)
	}
	if got != 0.4995 {
		t.Errorf("balance = %v", got)
	}
}

func TestGetClosedOrder(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v5/order/realtime" || q.Get("orderId") != "1001" || q.Get("openOnly") != "1" || q.Get("category") != "spot" {
			t.Errorf("got %s", r.URL)
		}
		checkSigned(t, r, r.URL.RawQuery)
		io.WriteString(w, ok(`{"list":[{"orderId":"1001","orderLinkId":"tb-buy","orderStatus":"Filled","avgPrice":"150.123","cumExecQty":"0.3330"}]}`))
	})

	o, found, err := c.GetClosedOrder("SOLUSDT", "1001")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !o.Closed() || o.AvgPrice != 150.123 || o.CumExecQty != 0.333 {
		t.Errorf("order = %+v", o)
	}
}

func TestStopOrdersListAndCancel(t *testing.T) {
	var cancelBody map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v5/order/realtime":
			if r.URL.Query().Get("orderFilter") != "StopOrder" || r.URL.Query().Get("openOnly") != "" {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
			io.WriteString(w, ok(`{"list":[{"orderId":"7","orderLinkId":"tb-SOLUSDT-1-tp","orderStatus":"Untriggered","avgPrice":"","cumExecQty":"0"}]}`))
		case "/v5/order/cancel":
			var raw string
			raw, cancelBody = decodeBody(t, r)
			checkSigned(t, r, raw)
			io.WriteString(w, ok(`{"orderId":"7"}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})

	orders, err := c.GetOpenStopOrders("SOLUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].OrderID != "7" || orders[0].Closed() {
		t.Fatalf("orders = %+v", orders)
	}

	if err := c.CancelStopOrder("SOLUSDT", "7"); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"category": "spot", "symbol": "SOLUSDT", "orderId": "7", "orderFilter": "StopOrder"}
	if !reflect.DeepEqual(cancelBody, want) {
		t.Errorf("cancel body = %v", cancelBody)
	}
}
