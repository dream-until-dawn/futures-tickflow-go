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
$('snapshot').textContent = '资料登记 '+(status.project_inventory?.registered_at_utc.slice(0,10)||status.as_of)+' · 行情末日见品种详情';
for (const [key,exchange] of Object.entries(status.exchanges)) {
  const products=status.products.filter(p=>p.exchange===key);
  const card=append($('exchanges'),'button',undefined,'exchange-card');card.type='button';card.dataset.exchange=key;
  append(card,'strong',exchange.short+' '+key);append(card,'small',exchange.type+' · '+products.length+'品种');
  append(card,'span',products.filter(p=>p.datasets?.some(r=>r.kind==='ohlc')).length+'个品种已取日线资料');
  card.addEventListener('click',()=>{$('exchange').value=key;render();});
  const option=append($('exchange'),'option',exchange.short+' '+key);option.value=key;
}
function rangeText(p) {return p.first ? p.first+' 至 '+p.last : '主目录未观察';}
function calendarText(p) {return p.calendar_ranges.length ? p.calendar_ranges[0].first+' 至 '+p.calendar_ranges.at(-1).last : '未获证产品历史范围';}
// 只按已取得的冻结资料映射来源；目录候选和完整空响应不能填入数据格。
function sourceCells(p) {return p.source_cells;}
function pageState(p) {const q=(p.datasets||[]).filter(r=>r.kind==='ohlc');return q.some(r=>['official-catalog','cffex-monthly','cffex-real-fixture'].includes(r.route))?'primary':q.length?'sample_only':'directory_only';}
function render() {
  const query=$('search').value.trim().toLowerCase(), exchange=$('exchange').value, state=$('state').value;
  const list=status.products.filter(p=> (!exchange||p.exchange===exchange) &&
    (!query||[p.key,p.code,p.name,p.vendor_name].join(' ').toLowerCase().includes(query)) &&
    (!state || (state==='difference' ? p.distinct_fields>0 : state==='calendar_gap' ? p.calendar_gap : (p.datasets?pageState(p):p.state)===state)));
  const sort=$('sort').value;
  if(sort!=='default') {const field={rows:'rows',differences:'distinct_fields',unrequested:'not_requested_on_directory_route'}[sort];list.sort((a,b)=>b[field]-a[field]||a.key.localeCompare(b.key));}
  $('products').replaceChildren();
  for(const p of list) {
    const row=append($('products'),'tr');row.dataset.product=p.key;
    append(row,'td',status.exchanges[p.exchange].short,'exchange-name');
    let cell=append(row,'td');const button=append(cell,'button',p.name,'product-title');button.type='button';button.addEventListener('click',()=>details(p));
    append(cell,'div',p.code,'mono sub');
    for(const sources of sourceCells(p)) {
      cell=append(row,'td');
      for(const source of sources) append(cell,'div',source,'source-name');
    }
    const captions=['交易所','品种','OHLC（日线）','成交量','持仓量','结算价','成交额','历史交易日历','分钟/现场','目录/生命周期','保证金/手续费/涨跌停'];
    [...row.children].forEach((cell,index)=>{cell.dataset.label=captions[index];});
  }
  $('visible-count').textContent='当前显示 '+list.length+' / '+status.products.length+' 品种 · 来源表示部分资料已取得，点击品种查看范围';
  $('empty').hidden=list.length!==0;
  document.querySelectorAll('.exchange-card').forEach(c=>c.classList.toggle('active',c.dataset.exchange===exchange));
}
function detailSection(title, texts) {const section=append($('detail-body'),'section',undefined,'detail-section');append(section,'h3',title);for(const text of texts)append(section,'p',text);return section;}
const kindLabels={ohlc:'日线 OHLC',volume:'成交量',open_interest:'持仓量',settlement:'结算价',pre_settlement:'前结算价',turnover:'成交额',calendar:'历史交易日历',minute:'分钟样本',live_frames:'现场原始帧',directory:'合约目录 / 生命周期快照',margin:'交易所保证金表',fees:'交易所手续费表',limits:'涨跌停参数表'};
const completenessLabels={observed_only:'部分观察，尚未证明历史完整',verified_calendar_range:'所列自然日连续，已核公布日程',candidate_directory:'候选目录，非官方挂牌全集',recorded_original_not_located:'旧验收记录；本轮原件暂未定位'};
function timeText(value) {return value ? new Date(value).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false})+'（东八区）' : '未知（无可核时间记录）';}
function coverageDetails(p) {
  detailSection('各类数据的覆盖、完整性和更新',['每条按数据类型与来源独立列出，重叠不相加。字段观察期、原件采集、归档及索引时间分开；最初采集时间未证时保留未知。业务参数日期只是快照日，不自动证明有效期或公司实收。']);
  for(const [kind,label] of Object.entries(kindLabels)) {
    const rows=(p.datasets||[]).filter(r=>r.kind===kind);
    if(!rows.length)continue;
    const section=detailSection(label,[]);
    for(const r of rows) {
      const card=append(section,'article',undefined,'dataset-card');append(card,'h4',r.source);
      const dl=append(card,'dl');
      const pairs=[['读取层级',r.read_level||'见资料说明'],['覆盖起止',(r.first||'未知')+' → '+(r.last||'未知')],
        ['范围口径',r.kind==='calendar'?'所列自然日历范围':r.kind==='directory'?'目录/快照观察期，不是挂牌有效区间':'本品种所收合约/样本并集，不表示每个合约全覆盖'],
        ['完整性',completenessLabels[r.completeness]||r.completeness],
        ['本来源记录',number(r.present_rows)+' 条有值 / '+number(r.rows)+' 条原记录'+(r.contracts?'；'+r.contracts+'个合约':'')+(r.observed_days?'；'+r.observed_days+'个观察日':'')+'；'+(r.valid_rows===null||r.valid_rows===undefined?'有效数未知':number(r.valid_rows)+' 条通过字段基本检查')],
        ['最后更新（原件）',r.last_acquired_date ? r.last_acquired_date+'（仅日期，来自旧说明）' : timeText(r.last_acquired_at)]];
      if(r.valid_first)pairs.push(['有效值起止',r.valid_first+' → '+r.valid_last]);
      if(r.missing_rows!==undefined)pairs.push(['缺失 / 异常',r.missing_rows+' 条缺失；'+r.invalid_or_zero_price_rows+' 条异常或零价格；零成交量本身可合法']);
      if(r.last_receipt_at)pairs.push(['归档回执',timeText(r.last_receipt_at)+(r.original_acquisition_unknown?'；包含仅本地留存事件，不能认证首次HTTP时间':'' )]);
      if(r.last_exported_at)pairs.push(['快照导出',timeText(r.last_exported_at)+'；不代替HTTP采集时刻']);
      if(r.last_indexed_at)pairs.push(['索引生成',timeText(r.last_indexed_at)+'；不是原件更新时刻']);
      pairs.push(['更新时间口径','原件取证回执；不是交易所发布时刻。无记录则未知，归档/索引另列。'],['全历史完整率','未知；未用官方挂牌全集/有效日历闭合分母']);
      for(const [key,value] of pairs){append(dl,'dt',key);append(dl,'dd',value);}
      append(card,'p',r.limitations,'sub');
      if(r.file)append(card,'code',r.file);
      if(r.evidence_shas?.length)append(card,'div','证据 SHA256 '+r.evidence_shas[0],'mono sub');
    }
  }
}
function details(p) {
  $('detail-title').textContent=p.name+' · '+p.key;$('detail-body').replaceChildren();
  coverageDetails(p);
  detailSection('原研究目录统计（仅该路线）',[
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

for(const item of status.project_inventory?.shared_documents||[]) {
  const box=append($('shared-list'),'details');const title=append(box,'summary',item.title);
  append(box,'p','原件取得 '+timeText(item.requested_at)+'；发布日 '+(item.publisher_date||'未知')+'。'+item.readback);
  append(box,'code','SHA256 '+item.sha256);
  if(item.local_guard_correction)append(box,'p',item.local_guard_correction);
  const url=new URL(item.url);
  if(url.protocol==='https:'&&url.hostname==='www.shfe.com.cn') {const link=append(box,'a','查看来源网页');link.href=url.href;link.rel='noopener noreferrer';}
}

if(status.project_inventory) $('inventory-scope').textContent='已登记 '+status.project_inventory.datasets.length+' 条分品种/类型/来源资料记录，核读 '+status.project_inventory.input_files.length+' 件输入；'+status.project_inventory.not_located.length+' 项旧原件待定位。登记日期不等于行情更新日期。'+(status.project_inventory.scope_review?.later_engine_update||'');
