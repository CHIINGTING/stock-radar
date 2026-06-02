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

function volRatioLabel(r) {
    if (!r || r === 0) return '-';
    return `${fmt(r, 1)}x`;
}

function bidAskLabel(r) {
    if (!r || r === 0) return '-';
    return fmt(r, 2);
}

/* ─── tab switching ────────────────────────────────────────── */

let currentTab = 'scanner';

function switchTab(tab) {
    currentTab = tab;
    ['scanner', 'watchlist', 'portfolio'].forEach(t => {
        document.getElementById(`panel-${t}`).style.display = t === tab ? '' : 'none';
        document.getElementById(`tab-${t}`).classList.toggle('active', t === tab);
    });
    document.getElementById('detailContent').innerHTML =
        '<p class="placeholder">點選股票查看完整技術分析</p>';
}

/* ─── refresh ──────────────────────────────────────────────── */

let stockData = [];
let positionData = [];

async function refresh() {
    await Promise.all([loadStocks(), loadPositions()]);
}

/* ─── Scanner + Watchlist ──────────────────────────────────── */

async function loadStocks() {
    const res = await fetch('/api/stocks');
    stockData = await res.json();

    // Scanner: sort by score descending
    const sorted = [...stockData].sort((a, b) => b.score - a.score);
    renderScanner(sorted);
    renderWatchlist(stockData);

    document.getElementById('lastUpdate').textContent =
        '更新：' + new Date().toLocaleTimeString('zh-TW');
}

function renderScanner(data) {
    const tbody = document.getElementById('scannerTable');
    tbody.innerHTML = '';
    data.forEach(s => {
        const tr = document.createElement('tr');
        tr.onclick = () => loadDetail(s.code);
        const changeCls = cls(s.change_pct);
        const a = actionSlug(s.action);
        const largeTag = s.large_order
            ? '<span class="large-order">大單</span>'
            : '';
        const volCls = volPatternCls(s.vol_pattern);
        tr.innerHTML = `
            <td>${s.code}</td>
            <td>${s.name}</td>
            <td>${fmt(s.price)}</td>
            <td class="${changeCls}">${sign(s.change_pct)}${fmt(s.change_pct)}%</td>
            <td><span class="score-badge score-${scoreTier(s.score)}">${s.score}</span></td>
            <td>${volRatioLabel(s.vol_ratio)}</td>
            <td><span class="vol-pattern ${volCls}">${s.vol_pattern || '-'}</span></td>
            <td>${largeTag}</td>
            <td>${bidAskLabel(s.bid_ask_ratio)}</td>
            <td><span class="${a}">${s.action}</span></td>
        `;
        tbody.appendChild(tr);
    });
}

function renderWatchlist(data) {
    const tbody = document.getElementById('watchlistTable');
    tbody.innerHTML = '';
    data.forEach(s => {
        const tr = document.createElement('tr');
        tr.onclick = () => loadDetail(s.code);
        const changeCls = cls(s.change);
        const a = actionSlug(s.action);
        tr.innerHTML = `
            <td>${s.code}</td>
            <td>${s.name}</td>
            <td>${fmt(s.price)}</td>
            <td class="${changeCls}">${changeLabel(s.change, s.change_pct)}</td>
            <td>${s.ma5 > 0 ? fmt(s.ma5) : '-'}</td>
            <td>${s.ma20 > 0 ? fmt(s.ma20) : '-'}</td>
            <td>${s.rsi14 > 0 ? fmt(s.rsi14, 1) : '-'}</td>
            <td>${s.score}</td>
            <td><span class="${a}">${s.action}</span></td>
        `;
        tbody.appendChild(tr);
    });
}

function scoreTier(score) {
    if (score >= 90) return 'high';
    if (score >= 70) return 'mid';
    return 'low';
}

function volPatternCls(p) {
    if (!p) return '';
    if (p === '價漲量增') return 'vp-bull';
    if (p === '價跌量增') return 'vp-bear';
    if (p === '價漲量縮') return 'vp-watch';
    return '';
}

/* ─── Portfolio ────────────────────────────────────────────── */

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
    // bind click
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
    const profitAmtSign = profitAmt >= 0 ? '+' : '';
    const changeCls = cls(p.change);

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
                <span class="${changeCls}" style="font-size:12px">${changeLabel(p.change, p.change_pct)}</span>
            </div>
            <div class="pos-metric">
                <span class="pos-metric-label">損益</span>
                <span class="pos-metric-value ${pCls}">${pSign}${fmt(p.profit_pct)}%</span>
                <span style="font-size:12px;color:#888">${profitAmtSign}${profitAmt} 元</span>
            </div>
            <div class="pos-metric">
                <span class="pos-metric-label">成本 / 評分</span>
                <span class="pos-metric-value">${fmt(p.entry)}</span>
                <span style="font-size:12px;color:#888">技術分 ${p.score}</span>
            </div>
        </div>

        <div class="pos-targets">
            <span class="target-label stop">停損 ${fmt(p.stop_loss)}</span>
            <span class="target-label t1">目標一 ${fmt(p.target1)}</span>
            <span class="target-label t2">目標二 ${fmt(p.target2)}</span>
            <span class="target-label rr">風報 1:${fmt(p.risk_reward)}</span>
        </div>
    </div>`;
}

function showPositionDetail(p) {
    const a = actionSlug(p.action);
    const pCls = cls(p.profit_pct);
    const pSign = sign(p.profit_pct);
    const changeCls = cls(p.change);
    const profitAmt = ((p.current - p.entry) * p.shares).toFixed(0);
    const profitAmtSign = profitAmt >= 0 ? '+' : '';

    document.getElementById('detailContent').innerHTML = `
        <h3>${p.name} (${p.code}) — 持倉詳情</h3>

        <div class="detail-grid">
            <div class="detail-card">
                <div class="label">現價 / 漲跌</div>
                <div class="value large">${fmt(p.current)}</div>
                <div class="${changeCls}">${changeLabel(p.change, p.change_pct)}</div>
            </div>
            <div class="detail-card">
                <div class="label">建議動作</div>
                <div class="value large"><span class="${a}">${p.action}</span></div>
                <div>技術評分 ${p.score}</div>
            </div>
            <div class="detail-card">
                <div class="label">持倉損益</div>
                <div class="value large ${pCls}">${pSign}${fmt(p.profit_pct)}%</div>
                <div style="font-size:13px;color:#888">
                    成本 ${fmt(p.entry)} × ${p.shares} 股<br>
                    損益 ${profitAmtSign}${profitAmt} 元
                </div>
            </div>
        </div>

        <div class="advice-box">
            <div class="advice-title">交易建議</div>
            <div class="advice-text">${p.advice}</div>
        </div>

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
                <div>MA5 &nbsp;<strong>${p.ma5 > 0 ? fmt(p.ma5) : '-'}</strong></div>
                <div>MA20 <strong>${p.ma20 > 0 ? fmt(p.ma20) : '-'}</strong></div>
                <div style="margin-top:4px">RSI14 <strong>${fmt(p.rsi14, 1)}</strong>
                    <span style="font-size:12px;color:#888">${p.rsi14 < 30 ? ' 超賣' : p.rsi14 > 70 ? ' 超買' : ''}</span>
                </div>
            </div>
            <div class="detail-card">
                <div class="label">量能</div>
                <div>量比 <strong>${volRatioLabel(p.vol_ratio)}</strong></div>
                <div>型態 <strong>${p.vol_pattern || '-'}</strong></div>
                ${p.large_order ? '<div style="color:#c00;font-weight:bold">大單介入</div>' : ''}
                ${p.bid_ask_ratio > 0 ? `<div>買賣比 <strong>${fmt(p.bid_ask_ratio)}</strong></div>` : ''}
            </div>
        </div>

        <div class="reasons">
            <h3>訊號依據</h3>
            ${p.reason && p.reason.length > 0
                ? '<ul>' + p.reason.map(r => `<li>${r}</li>`).join('') + '</ul>'
                : '<p style="color:#aaa">無觸發條件</p>'}
        </div>
    `;
    document.getElementById('detail').scrollIntoView({ behavior: 'smooth' });
}

/* ─── Stock detail (watchlist / scanner click) ─────────────── */

async function loadDetail(code) {
    const res = await fetch(`/api/analyze?code=${code}`);
    const d = await res.json();

    const a = actionSlug(d.action);
    const changeCls = cls(d.change);
    const volCls = volPatternCls(d.vol_pattern);

    document.getElementById('detailContent').innerHTML = `
        <h3>${d.name} (${d.code})</h3>

        <div class="detail-grid">
            <div class="detail-card">
                <div class="label">現價</div>
                <div class="value large">${fmt(d.price)}</div>
                <div class="${changeCls}">${changeLabel(d.change, d.change_pct)}</div>
            </div>
            <div class="detail-card">
                <div class="label">建議 / 評分</div>
                <div class="value large"><span class="${a}">${d.action}</span></div>
                <div>分數 ${d.score}</div>
            </div>
            <div class="detail-card">
                <div class="label">進場 / 停損 / 停利</div>
                <div class="value">${fmt(d.entry)}</div>
                <div style="font-size:13px;color:#888">
                    停損 ${fmt(d.stop_loss)} &nbsp;|&nbsp; 停利 ${fmt(d.take_profit)}
                </div>
            </div>
        </div>

        <div class="detail-grid">
            <div class="detail-card">
                <div class="label">均線</div>
                <div>MA5 &nbsp;<strong>${d.ma5 > 0 ? fmt(d.ma5) : '-'}</strong></div>
                <div>MA20 <strong>${d.ma20 > 0 ? fmt(d.ma20) : '-'}</strong></div>
                <div>MA60 <strong>${d.ma60 > 0 ? fmt(d.ma60) : '-'}</strong></div>
            </div>
            <div class="detail-card">
                <div class="label">KDJ / RSI</div>
                <div>K <strong>${fmt(d.k, 1)}</strong> &nbsp; D <strong>${fmt(d.d, 1)}</strong> &nbsp; J <strong>${fmt(d.j, 1)}</strong></div>
                <div style="margin-top:4px">RSI14 <strong>${fmt(d.rsi14, 1)}</strong>
                    <span style="font-size:12px;color:#888">${d.rsi14 < 30 ? ' 超賣' : d.rsi14 > 70 ? ' 超買' : ' 中性'}</span>
                </div>
            </div>
            <div class="detail-card">
                <div class="label">量能分析</div>
                <div>量比 <strong>${volRatioLabel(d.vol_ratio)}</strong></div>
                <div><span class="vol-pattern ${volCls}">${d.vol_pattern || '-'}</span></div>
                ${d.large_order ? '<div style="color:#c00;font-weight:bold;margin-top:4px">大單介入</div>' : ''}
                ${d.bid_ask_ratio > 0 ? `<div>買賣比 <strong>${bidAskLabel(d.bid_ask_ratio)}</strong></div>` : ''}
            </div>
        </div>

        <div class="reasons">
            <h3>訊號原因</h3>
            ${d.reasons && d.reasons.length > 0
                ? '<ul>' + d.reasons.map(r => `<li>${r}</li>`).join('') + '</ul>'
                : '<p style="color:#aaa">無觸發條件</p>'}
        </div>
    `;
    document.getElementById('detail').scrollIntoView({ behavior: 'smooth' });
}

/* ─── init ─────────────────────────────────────────────────── */

refresh();
setInterval(refresh, 60000);
