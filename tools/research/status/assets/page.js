'use strict';
const status = JSON.parse(document.getElementById('status-data').textContent);
const $ = id => document.getElementById(id);
const number = n => n.toLocaleString('zh-CN');
const stateLabels = {primary:'主目录有研究日线', sample_only:'仅其他路线样本', directory_only:'本页仅有目录'};

// 所有来源文字用textContent插入，原件名称不能变成HTML或执行代码。
function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}
function append(parent, tag, text, className) {const node=element(tag,text,className);parent.append(node);return node;}
function tag(parent, text, className='') {return append(parent,'span',text,'tag '+className);}
const summary = status.summary;
$('snapshot').textContent = '资料快照 '+status.as_of+' · 行情日期逐行列出';
const kpis = [
  ['品种',summary.products,'商品/航运 '+summary.commodity_and_shipping_products+' · 金融 '+summary.financial_products],
  ['供应商候选合约',summary.candidates,'不是官方历史挂牌全集'],
  ['主目录非空日线',summary.states.nonempty,'合约份数，不是完整率'],
  ['完整空响应',summary.states.empty_unavailable,'保留失败，不计覆盖'],
  ['本路线未请求',summary.states.not_requested_on_directory_route,'不等于项目从未采样'],
  ['主目录日线行',summary.rows,'跨合约行数，非交易日数']
];
for (const [label,value,note] of kpis) {const card=append($('kpis'),'div',undefined,'kpi');append(card,'small',label);append(card,'strong',number(value));append(card,'small',note);}
for (const [key,exchange] of Object.entries(status.exchanges)) {
  const products=status.products.filter(p=>p.exchange===key);
  const card=append($('exchanges'),'button',undefined,'exchange-card');card.type='button';card.dataset.exchange=key;
  append(card,'strong',exchange.short+' '+key);append(card,'small',exchange.type+' · '+products.length+'品种');
  append(card,'span','主目录 '+number(products.reduce((n,p)=>n+p.rows,0))+' 行');
  card.addEventListener('click',()=>{$('exchange').value=key;render();});
  const option=append($('exchange'),'option',exchange.short+' '+key);option.value=key;
}
function rangeText(p) {return p.first ? p.first+' 至 '+p.last : '主目录未观察';}
function calendarText(p) {return p.calendar_ranges.length ? p.calendar_ranges[0].first+' 至 '+p.calendar_ranges.at(-1).last : '未获证产品历史范围';}
function render() {
  const query=$('search').value.trim().toLowerCase(), exchange=$('exchange').value, state=$('state').value;
  const list=status.products.filter(p=> (!exchange||p.exchange===exchange) &&
    (!query||[p.key,p.code,p.name,p.vendor_name].join(' ').toLowerCase().includes(query)) &&
    (!state || (state==='difference' ? p.distinct_fields>0 : state==='calendar_gap' ? p.calendar_gap : p.state===state)));
  const sort=$('sort').value;
  if(sort!=='default') {const field={rows:'rows',differences:'distinct_fields',unrequested:'not_requested_on_directory_route'}[sort];list.sort((a,b)=>b[field]-a[field]||a.key.localeCompare(b.key));}
  $('products').replaceChildren();
  for(const p of list) {
    const row=append($('products'),'tr');row.dataset.product=p.key;
    let cell=append(row,'td');const button=append(cell,'button',p.name,'product-title');button.type='button';button.addEventListener('click',()=>details(p));
    append(cell,'div',p.key,'mono sub');append(cell,'div',p.category,'sub');
    cell=append(row,'td');append(cell,'strong',number(p.nonempty)+' / '+number(p.candidates)+' 合约');
    append(cell,'div','空 '+p.empty_unavailable+' · 本路线未请求 '+p.not_requested_on_directory_route,'sub');
    const meter=append(cell,'div',undefined,'meter');const fill=append(meter,'span');fill.style.width=(p.nonempty/p.candidates*100)+'%';
    append(cell,'div',p.source+' · '+number(p.rows)+'行','sub');
    cell=append(row,'td');append(cell,'div',rangeText(p));append(cell,'div','合约并集跨度；不是连续覆盖','sub');
    cell=append(row,'td');
    if(!p.reference_days) {tag(cell,'主目录未对照','muted-tag');append(cell,'div','没有参考不算零差异通过','sub');}
    else {append(cell,'div','参考 '+number(p.reference_days)+' 合约日');tag(cell,'待核 '+p.distinct_fields+' 字段',p.distinct_fields?'warn':'');append(cell,'div','缺参考 '+p.missing_reference_days+' 合约日；未报价 '+number(p.unpriced_fields)+' 字段','sub');}
    if(p.exchange==='CFFEX') tag(cell,'明文参考 · 未认证','warn');
    cell=append(row,'td');append(cell,'div',calendarText(p));append(cell,'div',p.calendar_ranges.length ? '仅制度记录范围；不授予价格资格' : '不套用当前模板补历史','sub');
    if(p.calendar_unknown_rows) append(cell,'div','实际行中 '+number(p.calendar_unknown_rows)+' 行日历未知','sub');
    cell=append(row,'td');append(cell,'div',p.supplementary.length ? p.supplementary.length+'份有限样本' : '本页未收录其他样本');
    append(cell,'div',p.supplementary.length ? [...new Set(p.supplementary.map(s=>s.source))].join('；') : '不能据此推断项目绝无其他数据','sub');
    cell=append(row,'td');tag(cell,stateLabels[p.state],p.state==='sample_only'?'sample':p.state==='directory_only'?'muted-tag':'');append(cell,'div','本目录仅研究用途','sub');
    const more=append(cell,'button','查看细节');more.type='button';more.addEventListener('click',()=>details(p));
    const captions=['品种','日线主目录','实际观察期','参考与质量','已有制度日历','其他已取样本','当前结论'];
    [...row.children].forEach((cell,index)=>{cell.dataset.label=captions[index];});
  }
  $('visible-count').textContent='当前显示 '+list.length+' / '+status.products.length+' 品种 · 主目录统计与其他样本分层，全部保留准入边界';
  $('empty').hidden=list.length!==0;
  document.querySelectorAll('.exchange-card').forEach(c=>c.classList.toggle('active',c.dataset.exchange===exchange));
}
function detailSection(title, texts) {const section=append($('detail-body'),'section',undefined,'detail-section');append(section,'h3',title);for(const text of texts)append(section,'p',text);return section;}
function details(p) {
  $('detail-title').textContent=p.name+' · '+p.key;$('detail-body').replaceChildren();
  detailSection('日线来源与完整性边界',[
    p.source+'；非空 '+p.nonempty+'，完整空响应 '+p.empty_unavailable+'，仅本路线未请求 '+p.not_requested_on_directory_route+'。',
    '实际观察 '+rangeText(p)+'，共 '+number(p.rows)+' 行；不是每个合约均覆盖此跨度，也不证明首末报价就是挂牌或最后交易日。',
    '价格列为OHLC，数量列为volume/open_oi/close_oi；零成交日与盘后量仓处理仍有待核。'
  ]);
  detailSection('对照、字段与置信度',[
    p.references.join('；') || '本目录无参考对照，不能宣称数据一致。',
    '相交参考 '+number(p.overlapping_days)+' 合约日；实际行中无本批参考 '+number(p.rows-p.overlapping_days)+' 行。',
    '待核 '+p.distinct_fields+' 数值字段；另有 '+p.binary64_equivalent_fields+' 项十进制不同但binary64同值；'+number(p.unpriced_fields)+' 个参考未报价字段。',
    p.settlement+'。'+p.turnover+'。',
    '字节身份和计数可复现；来源语义、制度、全生命周期、PIT及金融资格不能由此推定。'
  ]);
  const cal=detailSection('已有制度日历，不是价格准入',[calendarText(p)+'；未获证范围继续未知，不推算周末、假日或夜盘。']);
  const ul=append(cal,'ul');for(const r of p.calendar_ranges)append(ul,'li',r.first+'—'+r.last+'；'+r.open_records+'个开放记录；'+r.file);
  const extra=detailSection('其他已取得样本（不与主目录重复加总）',['这是已有样本元数据，不是本轮新增市场采集。未知日期、零结算值、错误行及版本差异没有据此修复。']);
  const wrapper=append(extra,'div',undefined,'detail-samples');const table=append(wrapper,'table');
  for(const s of p.supplementary) {const tr=append(table,'tr');append(tr,'td',s.source+' / '+s.symbol);append(tr,'td',s.rows+'行；'+s.first+'—'+s.last);append(tr,'td',s.comparison+(s.zero_settlement?'；零结算 '+s.zero_settlement:'')+(s.invalid_days?'；异常日 '+s.invalid_days:''));}
  if(!p.supplementary.length)append(extra,'p','本页未收录其他路线样本，不能作全项目“无数据”结论。');
  detailSection('当前结论',['仅研究定位；官方历史合约全集及逐合约挂牌/摘牌仍未证，完整回测资格不新增。','供应商中文简称：'+p.vendor_name+'；显示别名不作为官方规则证据。']);
  $('detail').showModal();$('close-detail').focus();
}
$('close-detail').addEventListener('click',()=>$('detail').close());
for(const id of ['exchange','state','sort']) $(id).addEventListener('change',render);
$('search').addEventListener('input',render);
$('reset').addEventListener('click',()=>{$('search').value='';$('exchange').value='';$('state').value='';$('sort').value='default';render();});
$('engine-status').textContent='已独立回读 '+number(status.engine_acceptance.nonempty_csvs)+' 份CSV、'+number(status.engine_acceptance.actual_rows)+'行并核首末日期，'+status.engine_acceptance.negative_controls+'个目标反例通过；上游再次重放对方脚本，报告逐字节一致。';
$('difference-note').textContent='主目录待核 '+summary.unresolved_fields+' 个字段；旧供应商间 '+summary.legacy_vendor_distinct_fields+' 项差异另册，不重复合并。';
$('catalog-sha').textContent='研究目录 SHA256 '+status.provenance.catalog_sha256;
render();
