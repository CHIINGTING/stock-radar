/* ─── helpers ─────────────────────────────────────────────── */

function fmt(n, d = 2) {
    if (n === undefined || n === null) return '-';
    return Number(n).toFixed(d);
}

function sign(v) { return v >= 0 ? '+' : ''; }

function cls(v) {
    if (v > 0) return 'up';
    if (v < 0) return 'down';
    return 'flat';
}

function changeLabel(change, pct) {
    const s = change >= 0 ? '+' : '';
    return `${s}${fmt(change)} (${s}${fmt(pct)}%)`;
}

function actionSlug(a) {
    return (a || 'wait').toLowerCase().replace(/ /g, '-');
}

function stageCls(stage) {
    const map = {
        'BREAKOUT':     'stage-breakout',
        'UPTREND':      'stage-uptrend',
        'LATE_UPTREND': 'stage-late',
        'DISTRIBUTION': 'stage-dist',
        'BASE':         'stage-base',
    };
    return map[stage] || 'stage-base';
}

function riskCls(risk) {
    if (risk === 'LOW')       return 'risk-low';
    if (risk === 'HIGH')      return 'risk-high';
    if (risk === 'VERY_HIGH') return 'risk-very-high';
    return 'risk-mid';
}

function limitBadge(s) {
    if (!s.limit_status) return '';
    const labels = {
        'LIMIT_UP':                 { cls: 'limit-up',    text: '漲停' },
        'LIMIT_DOWN':               { cls: 'limit-down',  text: '跌停' },
        'HOT_STOCK':                { cls: 'limit-hot',   text: '🔥連板' },
        'AVOID':                    { cls: 'limit-avoid', text: '⚠連跌' },
        'OPEN_LIMIT_UP':            { cls: 'limit-open',  text: s.open_limit_type || '開板' },
        'LIMIT_UP_CONSOLIDATION':   { cls: 'limit-consol',text: '整理候補' },
    };
    const info = labels[s.limit_status];
    if (!info) return '';
    return `<span class="limit-badge ${info.cls}">${info.text}</span>`;
}

function regulationBadge(reg) {
    if (!reg) return '';
    const dayStr = reg.remaining_days >= 0
        ? `解除倒數 ${reg.remaining_days} 天`
        : '已解除';
    return `<span class="warn-tag" title="${dayStr}，解除後均值+${reg.avg_gain_after}%">⚠ ${reg.type}</span>`;
}

function regulationDetail(reg) {
    if (!reg) return '';
    const ended = reg.remaining_days < 0;
    return `
    <div class="regulation-box">
        <div class="regulation-type">⚠ ${reg.type}</div>
        <div class="regulation-rows">
            ${reg.start_date ? `<span>開始：${reg.start_date}</span>` : ''}
            ${reg.end_date   ? `<span>解除：${reg.end_date}</span>` : ''}
            <span class="${ended ? 'flat' : 'up'}">${ended ? '已解除' : '解除倒數 ' + reg.remaining_days + ' 天'}</span>
            <span style="color:#888">歷史解除後均值 <strong>+${reg.avg_gain_after}%</strong></span>
        </div>
    </div>`;
}

/* ─── tab switching ────────────────────────────────────────── */

let currentTab = 'radar';

function switchTab(tab) {
    currentTab = tab;
    ['radar', 'scanner', 'watchlist', 'portfolio'].forEach(t => {
        document.getElementById(`panel-${t}`).style.display = t === tab ? '' : 'none';
        document.getElementById(`tab-${t}`).classList.toggle('active', t === tab);
    });
    document.getElementById('detailContent').innerHTML =
        '<p class="placeholder">點選股票查看完整分析</p>';
}

/* ─── data + refresh ───────────────────────────────────────── */

let stockData    = [];
let positionData = [];
let scannerData  = { stocks: [], sectors: [] };
let radarData    = [];

async function refresh() {
    await Promise.all([loadRadar(), loadStocks(), loadScanner(), loadPositions()]);
}

/* ─── Radar (intraday day-trading) ─────────────────────────── */

// tfLight maps a timeframe trend to a coloured traffic light.
function tfLight(t) {
    const map = {
        'Bullish':  { e: '🟢', c: 'up',   z: '偏多' },
        'Bearish':  { e: '🔴', c: 'down', z: '偏空' },
        'Breakout': { e: '🚀', c: 'up',   z: '突破' },
        'Neutral':  { e: '🟡', c: 'flat', z: '中性' },
    };
    const m = map[t] || map['Neutral'];
    return `<span class="tf-light ${m.c}" title="${m.z}">${m.e}</span>`;
}

// sparkline renders the bid/ask ratio series as a tiny inline SVG.
function sparkline(series) {
    if (!series || series.length < 2) return '';
    const vals = series.map(p => p.ratio);
    const max = Math.max(...vals), min = Math.min(...vals);
    const w = 64, h = 18, n = vals.length, span = (max - min) || 1;
    const pts = vals.map((v, i) => {
        const x = (i / (n - 1)) * w;
        const y = h - ((v - min) / span) * (h - 2) - 1;
        return `${x.toFixed(1)},${y.toFixed(1)}`;
    }).join(' ');
    return `<svg width="${w}" height="${h}" class="spark"><polyline points="${pts}" fill="none" stroke="currentColor" stroke-width="1.5"/></svg>`;
}

function flowCell(f) {
    if (!f || !f.samples) return '<span class="flat">累積中…</span>';
    const c = f.trend === '增強' ? 'up' : f.trend === '衰退' ? 'down' : 'flat';
    const arrow = f.trend === '增強' ? '↑' : f.trend === '衰退' ? '↓' : '→';
    return `<div class="flow-cell ${c}">
        <span class="spark-wrap ${c}">${sparkline(f.series)}</span>
        <span class="flow-label">${f.trend} ${arrow} <small>買賣比 ${fmt(f.ratio, 2)}</small></span>
    </div>`;
}

function volCell(s) {
    const fx = v => v > 0 ? fmt(v, 1) + 'x' : '-';
    const mf = s.main_force
        ? `<div class="main-force ${s.main_force === '主流股' ? 'mf-main' : 'mf-spec'}">${s.main_force}</div>`
        : '';
    return `<div class="vol-cell">
        <span>3分 <strong>${fx(s.volume_3m)}</strong></span>
        <span>5分 <strong>${fx(s.volume_5m)}</strong></span>
        <span>15分 <strong>${fx(s.volume_15m)}</strong></span>
        ${mf}
    </div>`;
}

async function loadRadar() {
    const res = await fetch('/api/radar');
    radarData = await res.json();
    renderRadar(radarData);
    document.getElementById('lastUpdate').textContent =
        '更新：' + new Date().toLocaleTimeString('zh-TW');
}

function renderRadar(data) {
    const tbody = document.getElementById('radarTable');
    tbody.innerHTML = '';
    if (!data || !data.length) {
        tbody.innerHTML = '<tr><td colspan="10" class="placeholder">無資料</td></tr>';
        return;
    }
    data.forEach(s => {
        const tr = document.createElement('tr');
        if (s.can_buy) tr.classList.add('radar-go');
        if (s.should_exit) tr.classList.add('radar-exit');
        tr.onclick = () => showRadarDetail(s);
        const a = actionSlug(s.action);
        const closed = s.closed ? '<span class="closed-tag">已收盤</span>' : '';
        tr.innerHTML = `
            <td>${s.code}</td>
            <td>${s.name} ${closed}</td>
            <td>${fmt(s.price)}</td>
            <td class="tf-cell">${tfLight(s.trend_15m)}</td>
            <td class="tf-cell">${tfLight(s.trend_5m)}</td>
            <td class="tf-cell">${tfLight(s.trend_3m)}</td>
            <td>${volCell(s)}</td>
            <td>${flowCell(s.flow)}</td>
            <td><span class="score-badge score-${scoreTier(s.score)}">${s.score}</span><br>${momentumChip(s.momentum)}</td>
            <td><span class="${a} action-badge">${s.action}</span></td>`;
        tbody.appendChild(tr);
    });
}

// momentumChip renders the momentum state as a coloured chip.
function momentumChip(m) {
    const map = {
        'STRONG':  { c: 'mom-strong',  z: '動能強' },
        'RISING':  { c: 'mom-rising',  z: '動能上升' },
        'FADING':  { c: 'mom-fading',  z: '動能衰退' },
        'DEAD':    { c: 'mom-dead',    z: '動能消失' },
        'NEUTRAL': { c: 'mom-neutral', z: '中性' },
    };
    const i = map[m] || map['NEUTRAL'];
    return `<span class="mom-chip ${i.c}">${i.z}</span>`;
}

// exitLadder renders the 4-level graduated exit ladder, highlighting the active level.
function exitLadder(level) {
    const levels = [
        { n: 1, t: 'L1 量縮 · 準備獲利了結' },
        { n: 2, t: 'L2 量縮+買盤退 · 獲利了結' },
        { n: 3, t: 'L3 跌破支撐 · 離場' },
        { n: 4, t: 'L4 趨勢翻空 · 強制出' },
    ];
    return `<div class="exit-ladder">${levels.map(l =>
        `<div class="el-step ${level >= l.n ? 'el-on el-on-' + l.n : ''}">${l.t}</div>`
    ).join('')}</div>`;
}

function showRadarDetail(s) {
    const a = actionSlug(s.action);
    const yn = (ok, yes, no, good) => {
        // good=true → green-good answer is the "ok" branch; default ok=positive(up).
        const okCls = good === false ? 'down' : 'up';
        const noCls = good === false ? 'up' : 'down';
        return ok ? `<span class="${okCls}">${yes}</span>` : `<span class="${noCls}">${no}</span>`;
    };

    document.getElementById('detailContent').innerHTML = `
        <div class="scanner-detail">
            <div class="sd-header">
                <div>
                    <h3 style="margin:0">${s.name} <span style="color:#888;font-weight:400">(${s.code})</span></h3>
                    <div style="margin-top:4px">現價 <strong>${fmt(s.price)}</strong> ${momentumChip(s.momentum)} ${s.closed ? '<span class="closed-tag">已收盤</span>' : ''}</div>
                </div>
                <span class="${a} action-badge" style="font-size:16px">${s.action}</span>
            </div>

            <div class="radar-q">
                <div class="rq-card">
                    <div class="rq-label">可以買嗎？</div>
                    <div class="rq-value">${yn(s.can_buy, '可進場', '先不買')} <span class="score-badge score-${scoreTier(s.score)}">${s.score}</span></div>
                </div>
                <div class="rq-card">
                    <div class="rq-label">現在是回測買點嗎？</div>
                    <div class="rq-value">${yn(s.pullback_buy, '回測買點', '非回測點')}</div>
                </div>
                <div class="rq-card">
                    <div class="rq-label">動能是否衰退？</div>
                    <div class="rq-value">${yn(s.momentum_fading, '動能衰退', '動能健康', false)}</div>
                </div>
                <div class="rq-card">
                    <div class="rq-label">是否應立即離場？</div>
                    <div class="rq-value">${yn(s.should_exit, '立即離場', '無須離場', false)}</div>
                </div>
            </div>

            <div class="sd-grid3">
                <div class="sd-card">
                    <div class="sd-label">三時間框</div>
                    <div>15分 ${tfLight(s.trend_15m)} &nbsp; 5分 ${tfLight(s.trend_5m)} &nbsp; 3分 ${tfLight(s.trend_3m)}</div>
                </div>
                <div class="sd-card">
                    <div class="sd-label">量能（量比）</div>
                    ${volCell(s)}
                </div>
                <div class="sd-card">
                    <div class="sd-label">買賣單流（近15分）</div>
                    ${flowCell(s.flow)}
                </div>
            </div>

            <div class="reasons">
                <h3>判讀</h3>
                ${s.reasons && s.reasons.length > 0
                    ? '<ul>' + s.reasons.map(r => `<li>${r}</li>`).join('') + '</ul>'
                    : '<p style="color:#aaa">無</p>'}
            </div>
        </div>`;
    document.getElementById('detail').scrollIntoView({ behavior: 'smooth' });
}

/* ─── Watchlist ────────────────────────────────────────────── */

async function loadStocks() {
    const res = await fetch('/api/stocks');
    stockData = await res.json();
    renderWatchlist(stockData);
    document.getElementById('lastUpdate').textContent =
        '更新：' + new Date().toLocaleTimeString('zh-TW');
}

function renderWatchlist(data) {
    const tbody = document.getElementById('watchlistTable');
    tbody.innerHTML = '';
    data.forEach(s => {
        const tr = document.createElement('tr');
        tr.onclick = () => loadDetail(s.code);
        const a = actionSlug(s.action);
        tr.innerHTML = `
            <td>${s.code}</td><td>${s.name}</td>
            <td>${fmt(s.price)}</td>
            <td class="${cls(s.change)}">${changeLabel(s.change, s.change_pct)}</td>
            <td>${s.ma5 > 0 ? fmt(s.ma5) : '-'}</td>
            <td>${s.ma20 > 0 ? fmt(s.ma20) : '-'}</td>
            <td>${s.rsi14 > 0 ? fmt(s.rsi14, 1) : '-'}</td>
            <td>${s.score}</td>
            <td><span class="${a}">${s.action}</span></td>`;
        tbody.appendChild(tr);
    });
}

/* ─── Scanner ──────────────────────────────────────────────── */

async function loadScanner() {
    const res = await fetch('/api/scanner');
    scannerData = await res.json();
    renderScannerSectors(scannerData.sectors || []);
    renderScannerStocks(scannerData.stocks || []);
}

function renderScannerSectors(sectors) {
    const el = document.getElementById('sectorRanking');
    if (!sectors.length) { el.innerHTML = ''; return; }
    el.innerHTML = `
        <div class="sector-title">族群排名</div>
        ${sectors.map((s, i) => `
        <div class="sector-row">
            <span class="sector-rank">#${i+1}</span>
            <span class="sector-name">${s.name}</span>
            <span class="sector-flow ${s.flow === '流入' ? 'flow-in' : s.flow === '流出' ? 'flow-out' : 'flow-flat'}">${s.flow}</span>
            <span class="sector-score">${s.score}</span>
        </div>`).join('')}`;
}

function renderScannerStocks(data) {
    const tbody = document.getElementById('scannerTable');
    tbody.innerHTML = '';
    data.forEach(s => {
        const tr = document.createElement('tr');
        tr.onclick = () => showScannerDetail(s);
        const a   = actionSlug(s.action);
        const stg = stageCls(s.stage);
        const rsk = riskCls(s.risk);
        const highTag = s.is_120d_high
            ? '<span class="high-tag">120新高</span>'
            : s.is_60d_high
            ? '<span class="high-tag">60新高</span>'
            : '';
        const limitTag = limitBadge(s);
        const regTag   = regulationBadge(s.regulation);
        tr.innerHTML = `
            <td>${s.code} ${regTag}</td>
            <td>${s.name}</td>
            <td>${fmt(s.price)}<br><small class="${cls(s.change_pct)}">${sign(s.change_pct)}${fmt(s.change_pct)}%</small></td>
            <td><span class="stage-badge ${stg}">${s.stage_zh}</span> ${highTag} ${limitTag}</td>
            <td><span class="score-badge score-${scoreTier(s.score)}">${s.score}</span></td>
            <td>${s.sector !== '其他' ? s.sector : '-'}${s.sector_rank > 0 ? ` <small>#${s.sector_rank}</small>` : ''}</td>
            <td>${s.holding}</td>
            <td><span class="${rsk}">${s.risk_zh}</span></td>
            <td><span class="${a}">${s.action}</span></td>`;
        tbody.appendChild(tr);
    });
}

function scoreTier(score) {
    if (score >= 90) return 'high';
    if (score >= 65) return 'mid';
    return 'low';
}

function showScannerDetail(s) {
    const a   = actionSlug(s.action);
    const stg = stageCls(s.stage);
    const rsk = riskCls(s.risk);
    const highLabel = s.is_120d_high ? '120日新高 ★' : s.is_60d_high ? '60日新高' : '';

    // Limit status section
    const limitSection = buildLimitSection(s);

    document.getElementById('detailContent').innerHTML = `
        <div class="scanner-detail">
            <div class="sd-header">
                <div>
                    <h3 style="margin:0">${s.name} <span style="color:#888;font-weight:400">(${s.code})</span></h3>
                    <div style="margin-top:4px;display:flex;gap:6px;flex-wrap:wrap">
                        ${limitBadge(s)}
                        ${regulationBadge(s.regulation)}
                    </div>
                </div>
                <span class="${a} action-badge">${s.action}</span>
            </div>

            <div class="sd-grid4">
                <div class="sd-card">
                    <div class="sd-label">評分</div>
                    <div class="sd-value"><span class="score-badge score-${scoreTier(s.score)}" style="font-size:22px;padding:4px 12px">${s.score}</span></div>
                </div>
                <div class="sd-card">
                    <div class="sd-label">階段</div>
                    <div class="sd-value"><span class="stage-badge ${stg}">${s.stage_zh}</span></div>
                    ${highLabel ? `<div style="font-size:11px;color:#e07020;margin-top:4px">${highLabel}</div>` : ''}
                </div>
                <div class="sd-card">
                    <div class="sd-label">族群 / 排名</div>
                    <div class="sd-value">${s.sector !== '其他' ? s.sector : '-'}</div>
                    ${s.sector_rank > 0 ? `<div style="font-size:13px">排名 <strong>#${s.sector_rank}</strong> &nbsp; <span class="${s.sector_flow==='流入'?'flow-in':s.sector_flow==='流出'?'flow-out':'flow-flat'}">${s.sector_flow}</span></div>` : ''}
                </div>
                <div class="sd-card">
                    <div class="sd-label">建議持有 / 風險</div>
                    <div class="sd-value">${s.holding}</div>
                    <div><span class="${rsk}" style="font-size:13px">風險 ${s.risk_zh}</span></div>
                </div>
            </div>

            ${limitSection}

            <div class="sd-grid3">
                <div class="sd-card">
                    <div class="sd-label">現價 / 漲跌</div>
                    <div class="sd-value" style="font-size:22px">${fmt(s.price)}</div>
                    <div class="${cls(s.change)}">${changeLabel(s.change, s.change_pct)}</div>
                </div>
                <div class="sd-card">
                    <div class="sd-label">均線</div>
                    <div>MA20 &nbsp;<strong>${s.ma20 > 0 ? fmt(s.ma20) : '-'}</strong></div>
                    <div>MA60 &nbsp;<strong>${s.ma60 > 0 ? fmt(s.ma60) : '-'}</strong></div>
                    <div>MA120 <strong>${s.ma120 > 0 ? fmt(s.ma120) : '-'}</strong></div>
                </div>
                <div class="sd-card">
                    <div class="sd-label">相對強度 (RS)</div>
                    <div class="sd-value" style="font-size:22px">${s.rs > 0 ? fmt(s.rs) : '-'}</div>
                    <div style="font-size:12px;color:#888">${s.rs > 1.2 ? '領先大盤' : s.rs > 0.8 ? '與大盤同步' : s.rs > 0 ? '落後大盤' : ''}</div>
                </div>
            </div>

            <div class="reasons">
                <h3>分析依據</h3>
                ${s.reasons && s.reasons.length > 0
                    ? '<ul>' + s.reasons.map(r => `<li>${r}</li>`).join('') + '</ul>'
                    : '<p style="color:#aaa">無觸發條件</p>'}
            </div>
        </div>`;
    document.getElementById('detail').scrollIntoView({ behavior: 'smooth' });
}

function buildLimitSection(s) {
    const hasLimit = s.limit_status || s.limit_up_days_5 > 0 || s.limit_down_days_5 > 0 || s.regulation;
    if (!hasLimit) return '';

    const parts = [];

    if (s.limit_up_days_5 > 0 || s.limit_down_days_5 > 0) {
        parts.push(`
        <div class="sd-card">
            <div class="sd-label">漲停 / 跌停（近5日）</div>
            <div style="display:flex;gap:12px;margin-top:4px">
                <span>漲停 <strong class="up">${s.limit_up_days_5}</strong> 次</span>
                <span>跌停 <strong class="down">${s.limit_down_days_5}</strong> 次</span>
            </div>
            ${s.open_limit_type ? `<div style="font-size:12px;color:#888;margin-top:4px">${s.open_limit_type}</div>` : ''}
        </div>`);
    }

    if (s.regulation) {
        const reg = s.regulation;
        const ended = reg.remaining_days < 0;
        parts.push(`
        <div class="sd-card regulation-card">
            <div class="sd-label">⚠ ${reg.type}</div>
            <div class="regulation-countdown ${ended ? '' : 'count-active'}">
                ${ended ? '已解除' : `解除倒數 <strong>${reg.remaining_days}</strong> 天`}
            </div>
            <div style="font-size:12px;color:#888;margin-top:4px">
                ${reg.start_date ? `開始 ${reg.start_date}` : ''}
                ${reg.end_date   ? ` → ${reg.end_date}` : ''}
            </div>
            <div style="font-size:13px;margin-top:6px">
                歷史解除後均值 <strong class="up">+${reg.avg_gain_after}%</strong>
            </div>
        </div>`);
    }

    if (parts.length === 0) return '';

    return `<div class="sd-grid3" style="margin-bottom:14px">${parts.join('')}</div>`;
}

/* ─── Portfolio ────────────────────────────────────────────── */

// momentumLadderBlock renders a holding's intraday momentum state and the
// graduated take-profit / exit ladder (動能導向，非固定停損).
function momentumLadderBlock(r) {
    if (!r) return '';
    const ma = actionSlug(r.action);
    return `
    <div class="pos-momentum">
        <div class="pos-mom-head">
            <span class="pos-metric-label">動能 / 離場階梯 ${r.closed ? '<span class="closed-tag">已收盤</span>' : ''}</span>
            <span>${momentumChip(r.momentum)} <span class="${ma} action-badge">${r.action}</span></span>
        </div>
        ${exitLadder(r.exit_level)}
    </div>`;
}

async function loadPositions() {
    const res = await fetch('/api/positions');
    positionData = await res.json();
    renderPortfolio(positionData);
}

function renderPortfolio(data) {
    const grid = document.getElementById('portfolioCards');
    if (!data || data.length === 0) {
        grid.innerHTML = '<p class="placeholder">尚無持倉資料（請在 stocks.yaml 設定 positions）</p>';
        return;
    }
    grid.innerHTML = data.map(p => positionCard(p)).join('');
    data.forEach(p => {
        const el = document.getElementById(`pos-${p.code}`);
        if (el) el.addEventListener('click', () => showPositionDetail(p));
    });
}

function positionCard(p) {
    const a = actionSlug(p.action);
    const pCls = cls(p.profit_pct);
    const pSign = sign(p.profit_pct);
    const profitAmt = ((p.current - p.entry) * p.shares).toFixed(0);
    const profitSign = profitAmt >= 0 ? '+' : '';
    return `
    <div class="pos-card" id="pos-${p.code}">
        <div class="pos-card-header">
            <div class="pos-title">${p.name} <span class="pos-code">${p.code}</span></div>
            <span class="${a} action-badge">${p.action}</span>
        </div>
        <div class="pos-advice">${p.advice}</div>
        <div class="pos-metrics">
            <div class="pos-metric">
                <span class="pos-metric-label">現價</span>
                <span class="pos-metric-value">${fmt(p.current)}</span>
                <span class="${cls(p.change)}" style="font-size:12px">${changeLabel(p.change, p.change_pct)}</span>
            </div>
            <div class="pos-metric">
                <span class="pos-metric-label">損益</span>
                <span class="pos-metric-value ${pCls}">${pSign}${fmt(p.profit_pct)}%</span>
                <span style="font-size:12px;color:#888">${profitSign}${profitAmt} 元</span>
            </div>
            <div class="pos-metric">
                <span class="pos-metric-label">成本 / 評分</span>
                <span class="pos-metric-value">${fmt(p.entry)}</span>
                <span style="font-size:12px;color:#888">技術分 ${p.score}</span>
            </div>
        </div>
        ${momentumLadderBlock(p.radar)}
        <div class="pos-targets">
            <span class="target-label stop">參考防線 ${fmt(p.radar && p.radar.stop_ref > 0 ? p.radar.stop_ref : p.stop_loss)}</span>
            <span class="target-label t1">目標一 ${fmt(p.target1)}</span>
            <span class="target-label t2">目標二 ${fmt(p.target2)}</span>
            <span class="target-label rr">風報 1:${fmt(p.risk_reward)}</span>
        </div>
    </div>`;
}

// positionMomentumSection renders the full intraday momentum / exit-ladder panel
// for a holding — the primary momentum-driven exit view (動能消失即離場).
function positionMomentumSection(r) {
    if (!r) return '';
    const ma = actionSlug(r.action);
    const yn = (ok, yes, no, badIsUp) => {
        const okCls = badIsUp ? 'down' : 'up';
        const noCls = badIsUp ? 'up' : 'down';
        return ok ? `<span class="${okCls}">${yes}</span>` : `<span class="${noCls}">${no}</span>`;
    };
    return `
    <div class="momentum-box">
        <div class="advice-title">動能管理 ${momentumChip(r.momentum)} <span class="${ma} action-badge">${r.action}</span> ${r.closed ? '<span class="closed-tag">已收盤</span>' : ''}</div>
        <div class="exit-ladder" style="margin:8px 0">
            ${[1,2,3,4].map(n => {
                const labels = {1:'L1 量縮·準備獲利了結',2:'L2 量縮+買盤退·獲利了結',3:'L3 跌破支撐·離場',4:'L4 趨勢翻空·強制出'};
                return `<div class="el-step ${r.exit_level >= n ? 'el-on el-on-' + n : ''}">${labels[n]}</div>`;
            }).join('')}
        </div>
        <div class="mom-flags">
            <span>三框 ${tfLight(r.trend_15m)}${tfLight(r.trend_5m)}${tfLight(r.trend_3m)}</span>
            <span>量縮 ${yn(r.volume_fade, '是', '否', true)}</span>
            <span>買盤退 ${yn(r.buy_flow_weakening, '是', '否', true)}</span>
            <span>破支撐 ${yn(r.break_5m_support, '是', '否', true)}</span>
            <span>立即離場 ${yn(r.should_exit, '是', '否', true)}</span>
        </div>
        <div style="font-size:12px;color:#888;margin-top:6px">買賣單流：${flowCell(r.flow)}</div>
        <div style="font-size:12px;color:#888;margin-top:4px">量縮＝獲利了結訊號（非停損）。參考防線 ${r.stop_ref > 0 ? fmt(r.stop_ref) : '-'}（非固定停損價）。</div>
    </div>`;
}

function showPositionDetail(p) {
    const a = actionSlug(p.action);
    const pCls = cls(p.profit_pct);
    const pSign = sign(p.profit_pct);
    const profitAmt = ((p.current - p.entry) * p.shares).toFixed(0);
    const profitSign = profitAmt >= 0 ? '+' : '';

    document.getElementById('detailContent').innerHTML = `
        <h3>${p.name} (${p.code}) — 持倉詳情</h3>
        <div class="detail-grid">
            <div class="detail-card">
                <div class="label">現價 / 漲跌</div>
                <div class="value large">${fmt(p.current)}</div>
                <div class="${cls(p.change)}">${changeLabel(p.change, p.change_pct)}</div>
            </div>
            <div class="detail-card">
                <div class="label">建議動作</div>
                <div class="value large"><span class="${a}">${p.action}</span></div>
                <div>技術評分 ${p.score}</div>
            </div>
            <div class="detail-card">
                <div class="label">持倉損益</div>
                <div class="value large ${pCls}">${pSign}${fmt(p.profit_pct)}%</div>
                <div style="font-size:13px;color:#888">成本 ${fmt(p.entry)} × ${p.shares} 股<br>損益 ${profitSign}${profitAmt} 元</div>
            </div>
        </div>
        <div class="advice-box">
            <div class="advice-title">交易建議</div>
            <div class="advice-text">${p.advice}</div>
        </div>
        ${positionMomentumSection(p.radar)}
        <div class="detail-grid">
            <div class="detail-card">
                <div class="label">停損 / 目標</div>
                <div style="color:#c00;font-weight:bold">停損 ${fmt(p.stop_loss)}</div>
                <div style="color:#080;font-weight:bold;margin-top:4px">目標一 ${fmt(p.target1)}</div>
                <div style="color:#00c;font-weight:bold">目標二 ${fmt(p.target2)}</div>
                <div style="margin-top:4px;font-size:12px;color:#888">風險報酬 1:${fmt(p.risk_reward)}</div>
            </div>
            <div class="detail-card">
                <div class="label">均線</div>
                <div>MA5  <strong>${p.ma5 > 0 ? fmt(p.ma5) : '-'}</strong></div>
                <div>MA20 <strong>${p.ma20 > 0 ? fmt(p.ma20) : '-'}</strong></div>
                <div style="margin-top:4px">RSI14 <strong>${fmt(p.rsi14, 1)}</strong>
                    <span style="font-size:12px;color:#888">${p.rsi14 < 30 ? ' 超賣' : p.rsi14 > 70 ? ' 超買' : ''}</span>
                </div>
            </div>
            <div class="detail-card">
                <div class="label">量能</div>
                <div>量比 <strong>${p.vol_ratio > 0 ? fmt(p.vol_ratio, 1)+'x' : '-'}</strong></div>
                <div>型態 <strong>${p.vol_pattern || '-'}</strong></div>
            </div>
        </div>
        <div class="reasons">
            <h3>訊號依據</h3>
            ${p.reason && p.reason.length > 0
                ? '<ul>' + p.reason.map(r => `<li>${r}</li>`).join('') + '</ul>'
                : '<p style="color:#aaa">無觸發條件</p>'}
        </div>`;
    document.getElementById('detail').scrollIntoView({ behavior: 'smooth' });
}

/* ─── Watchlist detail drawer ───────────────────────────────── */

async function loadDetail(code) {
    const res = await fetch(`/api/analyze?code=${code}`);
    const d = await res.json();
    const a = actionSlug(d.action);

    document.getElementById('detailContent').innerHTML = `
        <h3>${d.name} (${d.code})</h3>
        <div class="detail-grid">
            <div class="detail-card">
                <div class="label">現價</div>
                <div class="value large">${fmt(d.price)}</div>
                <div class="${cls(d.change)}">${changeLabel(d.change, d.change_pct)}</div>
            </div>
            <div class="detail-card">
                <div class="label">建議 / 評分</div>
                <div class="value large"><span class="${a}">${d.action}</span></div>
                <div>分數 ${d.score}</div>
            </div>
            <div class="detail-card">
                <div class="label">進場 / 停損 / 停利</div>
                <div class="value">${fmt(d.entry)}</div>
                <div style="font-size:13px;color:#888">停損 ${fmt(d.stop_loss)} &nbsp;|&nbsp; 停利 ${fmt(d.take_profit)}</div>
            </div>
        </div>
        <div class="detail-grid">
            <div class="detail-card">
                <div class="label">均線</div>
                <div>MA5 &nbsp;<strong>${d.ma5>0?fmt(d.ma5):'-'}</strong></div>
                <div>MA20 <strong>${d.ma20>0?fmt(d.ma20):'-'}</strong></div>
                <div>MA60 <strong>${d.ma60>0?fmt(d.ma60):'-'}</strong></div>
            </div>
            <div class="detail-card">
                <div class="label">KDJ / RSI</div>
                <div>K <strong>${fmt(d.k,1)}</strong> &nbsp;D <strong>${fmt(d.d,1)}</strong> &nbsp;J <strong>${fmt(d.j,1)}</strong></div>
                <div style="margin-top:4px">RSI14 <strong>${fmt(d.rsi14,1)}</strong>
                    <span style="font-size:12px;color:#888">${d.rsi14<30?' 超賣':d.rsi14>70?' 超買':' 中性'}</span>
                </div>
            </div>
            <div class="detail-card">
                <div class="label">量能</div>
                <div>量比 <strong>${d.vol_ratio>0?fmt(d.vol_ratio,1)+'x':'-'}</strong></div>
                <div>${d.vol_pattern||'-'}</div>
                ${d.large_order?'<div style="color:#c00;font-weight:bold">大單介入</div>':''}
            </div>
        </div>
        <div class="reasons">
            <h3>訊號原因</h3>
            ${d.reasons&&d.reasons.length>0
                ?'<ul>'+d.reasons.map(r=>`<li>${r}</li>`).join('')+'</ul>'
                :'<p style="color:#aaa">無觸發條件</p>'}
        </div>`;
    document.getElementById('detail').scrollIntoView({ behavior: 'smooth' });
}

/* ─── init ─────────────────────────────────────────────────── */

refresh();
setInterval(refresh, 60000);
// Radar is intraday — refresh it more often while that tab is open.
setInterval(() => { if (currentTab === 'radar') loadRadar(); }, 15000);
