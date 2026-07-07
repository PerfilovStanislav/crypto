import { createChart, CandlestickSeries, LineSeries, createSeriesMarkers } from 'lightweight-charts';
import type { IChartApi, UTCTimestamp } from 'lightweight-charts';
import { QuotesRequest, QuotesResponse, TimeframeEnum, IndicatorTypeEnum, SourceTypeEnum } from './api/quotes';

// Get DOM Elements
const form = document.getElementById('quotes-form') as HTMLFormElement;
const timeframeSelect = document.getElementById('select-timeframe') as HTMLSelectElement;
const takeprofitInput = document.getElementById('input-takeprofit') as HTMLInputElement;
const stoplossInput = document.getElementById('input-stoploss') as HTMLInputElement;

const ind1TypeSelect = document.getElementById('select-ind1-type') as HTMLSelectElement;
const ind1CoefInput = document.getElementById('input-ind1-coef') as HTMLInputElement;
const ind1SourceSelect = document.getElementById('select-ind1-source') as HTMLSelectElement;

const ind2TypeSelect = document.getElementById('select-ind2-type') as HTMLSelectElement;
const ind2CoefInput = document.getElementById('input-ind2-coef') as HTMLInputElement;
const ind2SourceSelect = document.getElementById('select-ind2-source') as HTMLSelectElement;

const chartContainer = document.getElementById('chart-container') as HTMLDivElement;
const errorOverlay = document.getElementById('error-overlay') as HTMLDivElement;
const errorMsg = document.getElementById('error-msg') as HTMLDivElement;
const loadingSpinner = document.getElementById('loading-spinner') as HTMLDivElement;
const statusIndicator = document.getElementById('status-indicator') as HTMLSpanElement;
const statusText = document.getElementById('status-text') as HTMLSpanElement;

// HUD fields
const hudSymbol = document.getElementById('hud-symbol') as HTMLDivElement;
const hudOpen = document.getElementById('hud-open') as HTMLSpanElement;
const hudHigh = document.getElementById('hud-high') as HTMLSpanElement;
const hudLow = document.getElementById('hud-low') as HTMLSpanElement;
const hudClose = document.getElementById('hud-close') as HTMLSpanElement;
const hudInd1 = document.getElementById('hud-ind1') as HTMLSpanElement;
const hudInd2 = document.getElementById('hud-ind2') as HTMLSpanElement;

// Application State
let chart: IChartApi | null = null;
let chartData: QuotesResponse | null = null;

// Format price helper
function formatPrice(val: number | undefined): string {
  if (val === undefined || isNaN(val)) return '--';
  return val.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 4 });
}

// Debounce helper to prevent flooding backend while typing
function debounce<T extends (...args: any[]) => void>(fn: T, delay: number) {
  let timer: any;
  return function (this: any, ...args: Parameters<T>) {
    clearTimeout(timer);
    timer = setTimeout(() => fn.apply(this, args), delay);
  };
}

// Show/Hide Overlays
function showLoading(show: boolean) {
  if (show) {
    loadingSpinner.classList.add('visible');
  } else {
    loadingSpinner.classList.remove('visible');
  }
}

function showError(msg: string | null) {
  if (msg) {
    errorMsg.innerText = msg;
    errorMsg.classList.add('error-state');
    errorOverlay.classList.add('visible');
    statusIndicator.className = 'status-indicator offline';
    statusText.innerText = 'Offline';
  } else {
    errorOverlay.classList.remove('visible');
    errorMsg.classList.remove('error-state');
    statusIndicator.className = 'status-indicator online';
    statusText.innerText = 'Connected';
  }
}

// Retrieve form parameters and build QuotesRequest object
function getRequestPayload(): QuotesRequest | null {
  const tf = parseInt(timeframeSelect.value) as TimeframeEnum;
  const takeprofit = parseFloat(takeprofitInput.value);
  const stoploss = parseFloat(stoplossInput.value);
  
  const ind1Type = parseInt(ind1TypeSelect.value) as IndicatorTypeEnum;
  const ind1Coef = parseFloat(ind1CoefInput.value);
  const ind1Source = parseInt(ind1SourceSelect.value) as SourceTypeEnum;

  const ind2Type = parseInt(ind2TypeSelect.value) as IndicatorTypeEnum;
  const ind2Coef = parseFloat(ind2CoefInput.value);
  const ind2Source = parseInt(ind2SourceSelect.value) as SourceTypeEnum;

  if (isNaN(tf) || isNaN(takeprofit) || isNaN(stoploss)) {
    return null;
  }
  if (isNaN(ind1Type) || isNaN(ind1Coef) || isNaN(ind1Source)) {
    return null;
  }
  if (isNaN(ind2Type) || isNaN(ind2Coef) || isNaN(ind2Source)) {
    return null;
  }

  return {
    tf,
    takeprofit,
    stoploss,
    ind1: {
      type: ind1Type,
      coef: ind1Coef,
      source: ind1Source
    },
    ind2: {
      type: ind2Type,
      coef: ind2Coef,
      source: ind2Source
    }
  };
}

// Fetch quotes from proxy API
async function fetchQuotes() {
  const payload = getRequestPayload();
  if (!payload) return;

  showLoading(true);
  statusIndicator.className = 'status-indicator syncing';
  statusText.innerText = 'Syncing...';

  try {
    const bodyBytes = QuotesRequest.encode(payload).finish();

    const response = await fetch('/api/get_quotes', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/x-protobuf',
        'Accept': 'application/x-protobuf'
      },
      body: bodyBytes
    });

    if (!response.ok) {
      throw new Error(`Server returned error status ${response.status}`);
    }

    const buffer = await response.arrayBuffer();
    const result = QuotesResponse.decode(new Uint8Array(buffer));
    
    chartData = result;
    showError(null);
    updateHUD(null); // Load default latest values
    drawChart();
  } catch (err: any) {
    console.error('Failed to fetch quotes:', err);
    showError(`Error communicating with backend: ${err.message || err}`);
  } finally {
    showLoading(false);
  }
}

const triggerFetch = debounce(fetchQuotes, 250);

// Update Top HUD values
function updateHUD(hoverData: { open?: number, high?: number, low?: number, close?: number, ind1?: number, ind2?: number } | null) {
  const symbol = chartData?.symbol || '--';
  hudSymbol.innerText = symbol;

  if (hoverData) {
    hudOpen.innerText = formatPrice(hoverData.open);
    hudHigh.innerText = formatPrice(hoverData.high);
    hudLow.innerText = formatPrice(hoverData.low);
    hudClose.innerText = formatPrice(hoverData.close);
    hudInd1.innerText = formatPrice(hoverData.ind1);
    hudInd2.innerText = formatPrice(hoverData.ind2);
    return;
  }

  // Set default (latest candle data)
  if (!chartData || !chartData.candles || !chartData.candles.c) {
    hudOpen.innerText = '--';
    hudHigh.innerText = '--';
    hudLow.innerText = '--';
    hudClose.innerText = '--';
    hudInd1.innerText = '--';
    hudInd2.innerText = '--';
    return;
  }

  const candles = chartData.candles;
  if (!candles || !candles.c || !candles.c.price) return;
  const numPoints = candles.c.price.length;

  const latestIdx = numPoints - 1;
  hudOpen.innerText = formatPrice(candles.o?.price[latestIdx]);
  hudHigh.innerText = formatPrice(candles.h?.price[latestIdx]);
  hudLow.innerText = formatPrice(candles.l?.price[latestIdx]);
  hudClose.innerText = formatPrice(candles.c?.price[latestIdx]);

  const ind1 = chartData.indicator1;
  const ind2 = chartData.indicator2;

  hudInd1.innerText = (ind1 && ind1.price && ind1.price.length > latestIdx) ? formatPrice(ind1.price[latestIdx]) : '--';
  hudInd2.innerText = (ind2 && ind2.price && ind2.price.length > latestIdx) ? formatPrice(ind2.price[latestIdx]) : '--';
}

// Chart Rendering Logic using TradingView Lightweight Charts
function drawChart() {
  if (!chartData || !chartData.candles) return;

  // Clear previous chart if exists
  if (chart) {
    chart.remove();
    chart = null;
  }

  const candles = chartData.candles;
  const ind1 = chartData.indicator1;
  const ind2 = chartData.indicator2;
  const times = chartData.time;
  const deals = chartData.deals;

  const oPrices = candles.o?.price || [];
  const hPrices = candles.h?.price || [];
  const lPrices = candles.l?.price || [];
  const cPrices = candles.c?.price || [];
  const n = cPrices.length;

  if (n === 0) return;

  // Create new chart instance
  chart = createChart(chartContainer, {
    width: chartContainer.clientWidth,
    height: chartContainer.clientHeight,
    layout: {
      background: { color: '#0b0e14' },
      textColor: '#94a3b8',
      fontFamily: 'Outfit, sans-serif',
    },
    grid: {
      vertLines: { color: '#1e293b' },
      horzLines: { color: '#1e293b' },
    },
    crosshair: {
      mode: 0, // normal mode showing both x & y lines
      vertLine: {
        color: 'rgba(148, 163, 184, 0.4)',
        width: 1,
        style: 3, // dashed
        labelBackgroundColor: '#1e293b',
      },
      horzLine: {
        color: 'rgba(148, 163, 184, 0.4)',
        width: 1,
        style: 3, // dashed
        labelBackgroundColor: '#1e293b',
      },
    },
    timeScale: {
      borderColor: '#1e293b',
      timeVisible: true,
      secondsVisible: false,
    },
    rightPriceScale: {
      borderColor: '#1e293b',
    },
  });

  // Add Candlestick Series
  const candlestickSeries = chart.addSeries(CandlestickSeries, {
    upColor: '#00f097',
    downColor: '#ff3860',
    borderVisible: false,
    wickUpColor: '#00f097',
    wickDownColor: '#ff3860',
  });

  // Prepare candle data
  const candleData = [];
  for (let i = 0; i < n; i++) {
    if (times[i]) {
      candleData.push({
        time: (times[i].getTime() / 1000) as UTCTimestamp,
        open: oPrices[i],
        high: hPrices[i],
        low: lPrices[i],
        close: cPrices[i]
      });
    }
  }
  candlestickSeries.setData(candleData);

  // Add Indicator 1 Series (Cyan)
  const lineSeries1 = chart.addSeries(LineSeries, {
    color: '#00f0ff',
    lineWidth: 2,
    crosshairMarkerVisible: true,
  });
  
  const ind1Data = [];
  if (ind1 && ind1.price) {
    for (let i = 0; i < n; i++) {
      const val = ind1.price[i];
      if (val !== undefined && !isNaN(val) && val > 0 && Math.abs(val) <= 90000000000000) {
        if (times[i]) {
          ind1Data.push({
            time: (times[i].getTime() / 1000) as UTCTimestamp,
            value: val
          });
        }
      }
    }
  }
  lineSeries1.setData(ind1Data);

  // Add Indicator 2 Series (Yellow)
  const lineSeries2 = chart.addSeries(LineSeries, {
    color: '#ffe600',
    lineWidth: 2,
    crosshairMarkerVisible: true,
  });

  const ind2Data = [];
  if (ind2 && ind2.price) {
    for (let i = 0; i < n; i++) {
      const val = ind2.price[i];
      if (val !== undefined && !isNaN(val) && val > 0 && Math.abs(val) <= 90000000000000) {
        if (times[i]) {
          ind2Data.push({
            time: (times[i].getTime() / 1000) as UTCTimestamp,
            value: val
          });
        }
      }
    }
  }
  lineSeries2.setData(ind2Data);

  // Populate simulated Deal markers (Buy/Sell indicators)
  const markers: any[] = [];
  if (deals && deals.length > 0) {
    deals.forEach((deal, idx) => {
      const openTimePoint = times[deal.open];
      if (openTimePoint) {
        const openTime = (openTimePoint.getTime() / 1000) as UTCTimestamp;
        markers.push({
          time: openTime,
          position: 'belowBar',
          color: '#00f097',
          shape: 'arrowUp',
          text: 'BUY',
          id: `buy-${idx}`
        });
      }

      const closeTimePoint = times[deal.close];
      if (closeTimePoint) {
        const closeTime = (closeTimePoint.getTime() / 1000) as UTCTimestamp;
        markers.push({
          time: closeTime,
          position: 'aboveBar',
          color: '#ff3860',
          shape: 'arrowDown',
          text: 'SELL',
          id: `sell-${idx}`
        });
      }
    });
  }

  // TV Lightweight Charts requires markers to be strictly sorted by time
  markers.sort((a, b) => (a.time as number) - (b.time as number));
  createSeriesMarkers(candlestickSeries, markers);

  // Subscribe to crosshair movement to update the top HUD overlay in real-time
  chart.subscribeCrosshairMove((param) => {
    if (!param.time) {
      updateHUD(null); // Reset to default
      return;
    }

    const candle = param.seriesData.get(candlestickSeries) as any;
    const line1 = param.seriesData.get(lineSeries1) as any;
    const line2 = param.seriesData.get(lineSeries2) as any;

    updateHUD({
      open: candle?.open,
      high: candle?.high,
      low: candle?.low,
      close: candle?.close,
      ind1: line1?.value,
      ind2: line2?.value
    });
  });

  // Auto-fit contents in timescale viewport
  chart.timeScale().fitContent();
}

// Bind Form inputs & Select elements change handlers
function setupFormListeners() {
  const inputs = form.querySelectorAll('input, select');
  inputs.forEach((input) => {
    input.addEventListener('change', () => {
      if (form.checkValidity()) {
        triggerFetch();
      }
    });

    if (input.tagName === 'INPUT') {
      input.addEventListener('input', () => {
        if (form.checkValidity()) {
          triggerFetch();
        }
      });
    }
  });
}

// Handle layout resize triggers using ResizeObserver
function setupResizeHandler() {
  const resizeObserver = new ResizeObserver((entries) => {
    if (entries.length === 0 || !chart) return;
    const { width, height } = entries[0].contentRect;
    chart.resize(width, height);
  });
  resizeObserver.observe(chartContainer);
}

// Initialize Application
function init() {
  setupFormListeners();
  setupResizeHandler();

  if (form.checkValidity()) {
    fetchQuotes();
  }
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', init);
} else {
  init();
}
