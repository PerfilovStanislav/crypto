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

// Stats HUD fields
const hudWins = document.getElementById('hud-stats-wins') as HTMLSpanElement;
const hudLosses = document.getElementById('hud-stats-losses') as HTMLSpanElement;
const hudProfit = document.getElementById('hud-stats-profit') as HTMLSpanElement;
const hudDrawdown = document.getElementById('hud-stats-drawdown') as HTMLSpanElement;

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
  const takeprofit = parseInt(takeprofitInput.value, 10);
  const stoploss = parseInt(stoplossInput.value, 10);
  
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

function getIndicatorTypeCode(type: IndicatorTypeEnum): string {
  switch (type) {
    case IndicatorTypeEnum.INDICATOR_TYPE_SMA: return 'sma';
    case IndicatorTypeEnum.INDICATOR_TYPE_EMA: return 'ema';
    case IndicatorTypeEnum.INDICATOR_TYPE_DEMA: return 'dema';
    case IndicatorTypeEnum.INDICATOR_TYPE_TEMA: return 'tema';
    case IndicatorTypeEnum.INDICATOR_TYPE_TEMA_ZERO: return 'temazero';
    default: return 'sma';
  }
}

function getSourceTypeCode(source: SourceTypeEnum): string {
  switch (source) {
    case SourceTypeEnum.SOURCE_TYPE_L: return 'l';
    case SourceTypeEnum.SOURCE_TYPE_O: return 'o';
    case SourceTypeEnum.SOURCE_TYPE_C: return 'c';
    case SourceTypeEnum.SOURCE_TYPE_H: return 'h';
    case SourceTypeEnum.SOURCE_TYPE_LO: return 'lo';
    case SourceTypeEnum.SOURCE_TYPE_LC: return 'lc';
    case SourceTypeEnum.SOURCE_TYPE_LH: return 'lh';
    case SourceTypeEnum.SOURCE_TYPE_OC: return 'oc';
    case SourceTypeEnum.SOURCE_TYPE_OH: return 'oh';
    case SourceTypeEnum.SOURCE_TYPE_CH: return 'ch';
    case SourceTypeEnum.SOURCE_TYPE_LOC: return 'loc';
    case SourceTypeEnum.SOURCE_TYPE_LOH: return 'loh';
    case SourceTypeEnum.SOURCE_TYPE_LCH: return 'lch';
    case SourceTypeEnum.SOURCE_TYPE_OCH: return 'och';
    default: return 'l';
  }
}

function updateURL() {
  const payload = getRequestPayload();
  if (!payload) return;

  const params = new URLSearchParams();
  const tfMap: Record<number, string> = {
    [TimeframeEnum.TIMEFRAME_1M]: '1m',
    [TimeframeEnum.TIMEFRAME_5M]: '5m',
    [TimeframeEnum.TIMEFRAME_15M]: '15m',
    [TimeframeEnum.TIMEFRAME_30M]: '30m',
    [TimeframeEnum.TIMEFRAME_1H]: '1h',
    [TimeframeEnum.TIMEFRAME_4H]: '4h',
    [TimeframeEnum.TIMEFRAME_1D]: '1d',
    [TimeframeEnum.TIMEFRAME_1W]: '1w'
  };
  params.set('tf', tfMap[payload.tf] || '4h');
  params.set('tp', Math.round(payload.takeprofit).toString());
  params.set('sl', Math.round(payload.stoploss).toString());

  if (payload.ind1) {
    const typeStr = getIndicatorTypeCode(payload.ind1.type);
    const sourceStr = getSourceTypeCode(payload.ind1.source);
    params.set('i1', `${typeStr},${payload.ind1.coef},${sourceStr}`);
  }
  if (payload.ind2) {
    const typeStr = getIndicatorTypeCode(payload.ind2.type);
    const sourceStr = getSourceTypeCode(payload.ind2.source);
    params.set('i2', `${typeStr},${payload.ind2.coef},${sourceStr}`);
  }

  const newRelativePathQuery = window.location.pathname + '?' + params.toString();
  window.history.replaceState(null, '', newRelativePathQuery);
}

// Fetch quotes from proxy API
async function fetchQuotes() {
  const payload = getRequestPayload();
  if (!payload) return;

  updateURL();

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

  // Set strategy statistics (deals, profit, drawdown)
  if (chartData) {
    hudWins.innerText = (chartData.wins !== undefined) ? chartData.wins.toString() : '--';
    hudLosses.innerText = (chartData.losses !== undefined) ? chartData.losses.toString() : '--';
    
    if (chartData.profitPct !== undefined) {
      const prof = chartData.profitPct;
      hudProfit.innerText = (prof >= 0 ? '+' : '') + prof.toFixed(2) + '%';
      hudProfit.style.color = prof >= 0 ? 'var(--color-bull)' : 'var(--color-bear)';
    } else {
      hudProfit.innerText = '--';
      hudProfit.style.color = 'var(--text-primary)';
    }
    
    if (chartData.maxDrawdownPct !== undefined) {
      hudDrawdown.innerText = chartData.maxDrawdownPct.toFixed(2) + '%';
    } else {
      hudDrawdown.innerText = '--';
    }
  } else {
    hudWins.innerText = '--';
    hudLosses.innerText = '--';
    hudProfit.innerText = '--';
    hudProfit.style.color = 'var(--text-primary)';
    hudDrawdown.innerText = '--';
  }

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

  // Populate simulated Deal markers (Open/Close indicators)
  const markers: any[] = [];
  if (deals && deals.length > 0) {
    deals.forEach((deal, idx) => {
      const openTimePoint = times[deal.open];
      if (openTimePoint) {
        const openTime = (openTimePoint.getTime() / 1000) as UTCTimestamp;
        markers.push({
          time: openTime,
          position: 'belowBar',
          color: '#3b82f6', // blue
          shape: 'arrowUp',
          text: 'OPEN',
          id: `open-${idx}`
        });
      }

      const closeTimePoint = times[deal.close];
      if (closeTimePoint) {
        const closeTime = (closeTimePoint.getTime() / 1000) as UTCTimestamp;
        
        let closeText = 'TP';
        let closeColor = '#48c774'; // green

        const candles = chartData?.candles;
        if (candles && candles.o && candles.o.price && candles.l && candles.l.price) {
          const openedPrice = candles.o.price[deal.open];
          const payload = getRequestPayload();
          if (payload) {
            const sl = payload.stoploss;
            const lowAtClose = candles.l.price[deal.close];
            if (lowAtClose <= openedPrice - sl) {
              closeText = 'SL';
              closeColor = '#ff3860'; // red
            }
          }
        }

        markers.push({
          time: closeTime,
          position: 'aboveBar',
          color: closeColor,
          shape: 'arrowDown',
          text: closeText,
          id: `close-${idx}`
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

  // Set default visible range to show only the last 150 candles
  const totalCandles = times.length;
  if (totalCandles > 150) {
    chart.timeScale().setVisibleLogicalRange({
      from: totalCandles - 150,
      to: totalCandles + 5 // add 5 bars of margin on the right
    });
  } else {
    chart.timeScale().fitContent();
  }
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
  const params = new URLSearchParams(window.location.search);
  const tfVal = params.get('tf');
  const tpVal = params.get('tp');
  const slVal = params.get('sl');
  const i1Val = params.get('i1');
  const i2Val = params.get('i2');

  const tfMap: Record<string, string> = {
    '1m': '1', '5m': '2', '15m': '3', '30m': '4', '1h': '5', '4h': '6', '1d': '7', '1w': '8',
    '1D': '7', '1W': '8'
  };

  const typeMap: Record<string, string> = {
    'sma': '1', 'ema': '2', 'dema': '3', 'tema': '4', 'temazero': '5', 'temaZero': '5'
  };

  const sourceMap: Record<string, string> = {
    'l': '1', 'o': '2', 'c': '3', 'h': '4',
    'lo': '5', 'lc': '6', 'lh': '7', 'oc': '8', 'oh': '9', 'ch': '10',
    'loc': '11', 'loh': '12', 'lch': '13', 'och': '14',
    'L': '1', 'O': '2', 'C': '3', 'H': '4',
    'LO': '5', 'LC': '6', 'LH': '7', 'OC': '8', 'OH': '9', 'CH': '10',
    'LOC': '11', 'LOH': '12', 'LCH': '13', 'OCH': '14'
  };

  if (tfVal) {
    const mapped = tfMap[tfVal] || tfMap[tfVal.toLowerCase()] || tfVal;
    if (mapped) timeframeSelect.value = mapped;
  }
  if (tpVal) takeprofitInput.value = tpVal;
  if (slVal) stoplossInput.value = slVal;

  if (i1Val) {
    const parts = i1Val.split(',');
    if (parts.length === 3) {
      const typeMapped = typeMap[parts[0]] || typeMap[parts[0].toLowerCase()] || parts[0];
      const sourceMapped = sourceMap[parts[2]] || sourceMap[parts[2].toLowerCase()] || parts[2];
      ind1TypeSelect.value = typeMapped;
      ind1CoefInput.value = parts[1];
      ind1SourceSelect.value = sourceMapped;
    }
  }

  if (i2Val) {
    const parts = i2Val.split(',');
    if (parts.length === 3) {
      const typeMapped = typeMap[parts[0]] || typeMap[parts[0].toLowerCase()] || parts[0];
      const sourceMapped = sourceMap[parts[2]] || sourceMap[parts[2].toLowerCase()] || parts[2];
      ind2TypeSelect.value = typeMapped;
      ind2CoefInput.value = parts[1];
      ind2SourceSelect.value = sourceMapped;
    }
  }

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
