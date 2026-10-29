package exchange

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

func TestMarketOrderRequest(t *testing.T) {
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
		io.WriteString(w, ok(`{"orderId":"1001","orderLinkId":"tb-SOLUSDT-S-1-open"}`))
	})

	id, err := c.PlaceOrder(MarketOrder("SOLUSDT", "Sell", "0.3", 2, false, "tb-SOLUSDT-S-1-open"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "1001" {
		t.Errorf("order id = %q", id)
	}

	want := map[string]any{
		"category":    "linear",
		"symbol":      "SOLUSDT",
		"side":        "Sell",
		"orderType":   "Market",
		"qty":         "0.3", // SOL
		"positionIdx": 2.0,   // hedge mode, short side
		"orderLinkId": "tb-SOLUSDT-S-1-open",
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %v\nwant  %v", body, want)
	}
}

func TestSetTradingStop(t *testing.T) {
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/position/trading-stop" {
			t.Errorf("got %s", r.URL)
		}
		var raw string
		raw, body = decodeBody(t, r)
		checkSigned(t, r, raw)
		io.WriteString(w, ok(`{}`))
	})

	if err := c.SetTradingStop("SOLUSDT", 1, "170.12", "135.12"); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"category":    "linear",
		"symbol":      "SOLUSDT",
		"tpslMode":    "Full",
		"positionIdx": 1.0,
		"takeProfit":  "170.12",
		"stopLoss":    "135.12",
		"tpTriggerBy": "LastPrice",
		"slTriggerBy": "LastPrice",
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %v\nwant  %v", body, want)
	}
}

func TestRetCodeIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"retCode":170131,"retMsg":"Insufficient balance.","result":{}}`)
	})

	_, err := c.PlaceOrder(MarketOrder("SOLUSDT", "Buy", "0.3", 1, false, "x"))
	if err == nil || !strings.Contains(err.Error(), "170131") || !isRetCode(err, 170131) {
		t.Fatalf("err = %v, want retCode 170131", err)
	}
}

func TestSetupHedge(t *testing.T) {
	var switchBody, leverageBody map[string]any
	switchCode := 110025 // already in hedge mode
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, body := decodeBody(t, r)
		checkSigned(t, r, raw)
		switch r.URL.Path {
		case "/v5/position/switch-mode":
			switchBody = body
			fmt.Fprintf(w, `{"retCode":%d,"retMsg":"Position mode is not modified","result":{}}`, switchCode)
		case "/v5/position/set-leverage":
			leverageBody = body
			io.WriteString(w, ok(`{}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})

	if err := c.SetupHedge("SOLUSDT"); err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"category": "linear", "symbol": "SOLUSDT", "mode": 3.0}; !reflect.DeepEqual(switchBody, want) {
		t.Errorf("switch-mode body = %v", switchBody)
	}
	if want := map[string]any{"category": "linear", "symbol": "SOLUSDT", "buyLeverage": "1", "sellLeverage": "1"}; !reflect.DeepEqual(leverageBody, want) {
		t.Errorf("set-leverage body = %v", leverageBody)
	}

	// Any other refusal (e.g. an open one-way position) must stop the bot.
	switchCode = 110024
	if err := c.SetupHedge("SOLUSDT"); err == nil {
		t.Fatal("want error")
	}
}

func TestGetPosition(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/position/list" || r.URL.RawQuery != "category=linear&symbol=SOLUSDT" {
			t.Errorf("got %s", r.URL)
		}
		checkSigned(t, r, r.URL.RawQuery)
		io.WriteString(w, ok(`{"category":"linear","list":[
			{"symbol":"SOLUSDT","positionIdx":1,"side":"Buy","size":"0.5","stopLoss":"135.12"},
			{"symbol":"SOLUSDT","positionIdx":2,"side":"","size":"0","stopLoss":"0.00"}]}`))
	})

	for idx, want := range map[int]Position{1: {Size: 0.5, StopLoss: 135.12}, 2: {}} {
		got, err := c.GetPosition("SOLUSDT", idx)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("positionIdx %d: %+v, want %+v", idx, got, want)
		}
	}
}

func TestHTTPErrorIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})

	if _, err := c.GetFreeBalance("USDT"); err == nil {
		t.Fatal("want error")
	}
}

func TestGetKlinesOldestFirstInUTC(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/market/kline" {
			t.Errorf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("interval") != "240" || q.Get("category") != "linear" || q.Get("symbol") != "SOLUSDT" || q.Get("limit") != "1000" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if r.Header.Get("X-BAPI-SIGN") != "" {
			t.Error("public endpoint must not be signed")
		}
		// newest first, like Bybit
		io.WriteString(w, ok(`{"category":"linear","symbol":"SOLUSDT","list":[
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
		if r.URL.Query().Get("category") != "linear" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		// real SOLUSDT perpetual values
		io.WriteString(w, ok(`{"category":"linear","list":[{"symbol":"SOLUSDT","baseCoin":"SOL","quoteCoin":"USDT",
			"lotSizeFilter":{"maxOrderQty":"79770.0","minOrderQty":"0.1","qtyStep":"0.1","minNotionalValue":"5"},
			"priceFilter":{"tickSize":"0.010"}}]}`))
	})

	inst, err := c.GetInstrument("SOLUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if inst.BaseCoin != "SOL" || inst.QuoteCoin != "USDT" || inst.MinOrderQty != 0.1 || inst.MinNotional != 5 {
		t.Errorf("instrument = %+v", inst)
	}

	checks := []struct{ got, want string }{
		{inst.FormatPrice(170.123), "170.12"},
		{inst.FormatPrice(135.125001), "135.13"},
		{inst.FormatPrice(170), "170.00"},
		{inst.FormatQty(0.49), "0.4"},             // floor to the qty step
		{inst.FormatQty(0.3), "0.3"},              // 0.3/0.1 is 2.9999...
		{inst.FormatQty(inst.MinQty(150)), "0.1"}, // minOrderQty: 0.1 SOL is already 15 USDT
		{inst.FormatQty(inst.MinQty(20)), "0.3"},  // 5 USDT at 20 needs 0.25, up to the step
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("got %s, want %s", c.got, c.want)
		}
	}
}

func TestGetFreeBalance(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/account/wallet-balance" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.RawQuery != "accountType=UNIFIED&coin=USDT" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		checkSigned(t, r, r.URL.RawQuery)
		io.WriteString(w, ok(`{"list":[{"accountType":"UNIFIED","coin":[{"coin":"USDT",
			"walletBalance":"1000","totalPositionIM":"400.5","totalOrderIM":"","locked":"0"}]}]}`))
	})

	// 400.5 USDT is the margin of an open position; "" is how UTA reports 0.
	got, err := c.GetFreeBalance("USDT")
	if err != nil {
		t.Fatal(err)
	}
	if got != 599.5 {
		t.Errorf("free balance = %v, want 599.5", got)
	}
}

func TestGetClosedOrder(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v5/order/realtime" || q.Get("orderId") != "1001" || q.Get("openOnly") != "1" || q.Get("category") != "linear" {
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
			if q := r.URL.Query(); q.Get("orderFilter") != "StopOrder" || q.Get("category") != "linear" || q.Get("openOnly") != "" {
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
	want := map[string]any{"category": "linear", "symbol": "SOLUSDT", "orderId": "7"}
	if !reflect.DeepEqual(cancelBody, want) {
		t.Errorf("cancel body = %v", cancelBody)
	}
}

func TestGetFilledOrders(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v5/order/history" || q.Get("orderStatus") != "Filled" || q.Get("category") != "linear" || q.Get("symbol") != "SOLUSDT" {
			t.Errorf("got %s", r.URL)
		}
		checkSigned(t, r, r.URL.RawQuery)
		io.WriteString(w, ok(`{"list":[{"orderId":"9","orderLinkId":"","orderStatus":"Filled","avgPrice":"135.1","cumExecQty":"0.3333",
			"positionIdx":1,"stopOrderType":"StopLoss","updatedTime":"1760529600000"}]}`))
	})

	orders, err := c.GetFilledOrders("SOLUSDT")
	if err != nil {
		t.Fatal(err)
	}
	want := Order{OrderID: "9", Status: "Filled", AvgPrice: 135.1, CumExecQty: 0.3333,
		PositionIdx: 1, StopOrderType: "StopLoss", UpdatedTime: time.UnixMilli(1760529600000).UTC()}
	if len(orders) != 1 || orders[0] != want {
		t.Errorf("orders = %+v", orders)
	}
}
