'use strict';
const status = JSON.parse(document.getElementById('status-data').textContent);
const $ = id => document.getElementById(id);
const number = n => n.toLocaleString('zh-CN');
const gateLabel = state => ({pass:'通过（限所列范围）',pending:'待补证',not_met:'未达到'}[state]||'待核算');
const gateItemLabel = name => ({history_ohlc:'长期日线OHLC',current_margin:'当前保证金率',effective_min_open:'实际最低开仓手数',settlement:'有效结算价',daily_update:'按交易日抓取更新'}[name]||name);
const dateLabel = day => day ? String(day).replace(/^(\d{4})(\d{2})(\d{2})$/,'$1-$2-$3') : '未知';
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
    (!state || (state==='difference' ? (p.distinct_fields>0||(p.extension_quality?.distinct_fields||0)>0) : state==='calendar_gap' ? p.calendar_gap : (p.datasets?pageState(p):p.state)===state)));
  const sort=$('sort').value;
  if(sort!=='default') {const field={rows:'rows',differences:'distinct_fields',unrequested:'not_requested_on_directory_route'}[sort];list.sort((a,b)=>b[field]-a[field]||a.key.localeCompare(b.key));}
  $('products').replaceChildren();
  for(const p of list) {
    const row=append($('products'),'tr');row.dataset.product=p.key;
    append(row,'td',status.exchanges[p.exchange].short,'exchange-name');
    let cell=append(row,'td');const button=append(cell,'button',p.name,'product-title');button.type='button';button.addEventListener('click',()=>details(p));
    append(cell,'div',p.code,'mono sub');
    if(p.price_delivery)append(cell,'div',p.price_delivery.consumer_status==='accepted_with_limits'?'真实价格包 · 离线验收见详情':'真实价格包 · 验收状态见详情','sub');
    for(const sources of sourceCells(p)) {
      cell=append(row,'td');
      for(const source of sources) append(cell,'div',source,'source-name');
    }
    if(p.user_data_gates) {
      append(row,'td',gateLabel(p.user_data_gates.backtest.status));
      append(row,'td',gateLabel(p.user_data_gates.daily_signal.status));
    }
    const captions=['交易所','品种','OHLC（日线）','成交量','持仓量','结算价','成交额','历史交易日历','分钟/现场','目录/生命周期','保证金/手续费/涨跌停','回测数据门槛','逐日信号数据门槛'];
    [...row.children].forEach((cell,index)=>{cell.dataset.label=captions[index];});
  }
  $('visible-count').textContent='当前显示 '+list.length+' / '+status.products.length+' 品种 · 来源表示部分资料已取得，点击品种查看范围';
  $('empty').hidden=list.length!==0;
  document.querySelectorAll('.exchange-card').forEach(c=>c.classList.toggle('active',c.dataset.exchange===exchange));
}
function detailSection(title, texts) {const section=append($('detail-body'),'section',undefined,'detail-section');append(section,'h3',title);for(const text of texts)append(section,'p',text);return section;}
const kindLabels={ohlc:'日线 OHLC',volume:'成交量',open_interest:'持仓量',settlement:'结算价',pre_settlement:'前结算价',turnover:'成交额',calendar:'历史交易日历',minute:'分钟样本',live_frames:'现场原始帧',directory:'合约目录 / 生命周期快照',margin:'交易所保证金表',fees:'交易所手续费表',limits:'涨跌停参数表'};
const completenessLabels={observed_contracts_only:'日表出现合约，不是挂牌全集',observed_only:'部分观察，尚未证明历史完整',verified_calendar_range:'所列自然日连续，已核公布日程',candidate_directory:'候选目录，非官方挂牌全集',recorded_original_not_located:'旧验收记录；本轮原件暂未定位'};
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
      if(r.comparison_pending)append(card,'p','本轮新路线对账：相交 '+r.comparison_pending.overlapping_days+' 合约日；'+r.comparison_pending.distinct_fields+' 个字段数值差异、'+r.comparison_pending.missing_official_days+' 个官方观察日期未出现在该EDB样本；另列待核，不覆盖旧差异。','sub');
      if(r.contract_observations?.length) {
        const list=append(card,'details',undefined,'contract-list');append(list,'summary','查看实际出现的 '+r.contract_observations.length+' 个合约');
        for(const c of r.contract_observations)append(list,'p',(c.canonical||c.symbol)+(c.canonical?'':'（原生码，年份未获证）')+'；观察 '+c.first+' → '+c.last+'；'+c.observed_days+'个日表记录日');
      }
      if(r.file)append(card,'code',r.file);
      if(r.evidence_shas?.length)append(card,'div','证据 SHA256 '+r.evidence_shas[0],'mono sub');
    }
  }
}
function details(p) {
  $('detail-title').textContent=p.name+' · '+p.key;$('detail-body').replaceChildren();
  if(['CZCE.RM','SHFE.hc'].includes(p.key)) {
    const extra=detailSection('11月18日官方原生观察补充批次',[
      '已将2021-01-04至2026-11-13的官方原生合约观察、结算、量仓及隔离行另行冻结；已完成限定独立回读，历史准入仍待证。',
      '此为旧原件重新整理，不是11月18日行情更新。观察候选不是上市全集；历史金融规则、因果换月及正式交易起点尚未认证。'
    ]);
    const link=append(extra,'a','查看逐年统计、原件链和缺项');link.href='rm-hc-evidence-handoff-20261118.md';
    if(p.key==='SHFE.hc')append(extra,'p','另有2021-01-04至2026-11-03的1,412日、4,236份官方业务表，含原始保证金、费用及上市到期字段；第二批待独立复核。表日不自动等于生效时刻，最后8个行情日缺参数。');
  }
  if(p.price_delivery) {
    const d=p.price_delivery, state={pending:'已成包，等待引擎独立验收',accepted_with_limits:'已按限定范围完成离线验收',blocked:'存在待解决反馈'}[d.consumer_status];
    const section=detailSection('真实价格交付包',[
      state+'；'+number(d.rows)+'条历史价格、'+d.historical_contracts+'个历史具体合约，当前保证金范围'+d.current_contracts+'约。',
      '交付观察 '+dateLabel(d.coverage.first)+' → '+dateLabel(d.coverage.last)+'；逐年日期 '+Object.entries(d.coverage.valid_days_by_year).map(([y,n])=>y+'年'+n+'日').join('、')+'。观察并集不是完整率。',
      d.official_earlier_prefix?'包含日期不重叠的官方早期/整缺年补充，具体范围见交付记录。':'每行绑定实际源响应；不拼主连、不补造缺日。',
      '包内保证金对应 '+dateLabel(d.parameter_day)+'，回放历史属于情景，公司标准不等于账户实收；未知实际最低开仓不补1手。资料交付验收不等于策略已就绪或生产已部署。'
    ]);
    append(section,'p','交付登记（派生，不是行情更新）：'+timeText(d.delivery_registered_at)+'。');
    const technical=append(section,'details');append(technical,'summary','查看交付文件及校验标识');
    append(technical,'p','子包 '+d.exchange+'；历史文件 '+d.history_artifact+'；manifest SHA256 '+d.manifest_sha256);
  }
  for(const ref of p.parameter_reference_supplements||[])detailSection('供应商编制参数观察（不等于真实专项已核）',[
    ref.publisher+'；来源标示更新日 '+ref.source_snapshot_day+'；'+ref.rows+'个具名合约。置信度中等，仅编制参考。',
    '已有字段 '+ref.available_reference_fields.join('、')+'；范围 '+ref.canonical_contracts.join('、')+'。',
    '最后取得 '+ref.retrieved_at+'；没有完整日线OHLC或目标日结算价。没有填入实际最低开仓空值或当前保证金硬项。',
    '来源 '+ref.source_url+'；SHA256 '+ref.source_sha256
  ]);
  for(const ref of p.margin_reference_supplements||[])detailSection('额外公司比例参考（不改变当前门槛）',[
    ref.publisher+'；资料标示日 '+ref.source_snapshot_day+' → '+ref.source_snapshot_day+'；生效交易日未知，不当作17日或下一日实收。',
    '已读 '+ref.rows+' 条临近交割合约参考；原生代码 '+ref.source_natives.join('、')+'。列为投机/套保，不是多/空。',
    '最后取得 '+ref.retrieved_at+'；完整原件已核，适用性待核。此表没有结算价，不能据标题满足逐日信号门槛。',
    '来源 '+ref.source_url+'；SHA256 '+ref.source_sha256
  ]);
  const constraintEvidence=status.constraint_supplement;
  const broker=p.broker_margin_reference;
  if(broker)detailSection('五矿期货17日公开保证金标准（参考情景）',[
    p.exchange==='CFFEX'?'金融品种单列：本项不属于五商品场所交接范围，不进入引擎商品投影。':'本项属于五商品场所；交引擎的商品投影另行排除金融8品种/28条新增公司记录。',
    '覆盖日期：20261117 → 20261117；该公司公开投机标准 '+broker.company_spec_ratio+'、套保标准 '+broker.company_hedge_ratio+'（比例）。不是交易所原件或本账户实收，不替换已有交易所多空参数。',
    '特殊月份优先：'+(broker.special.map(s=>s.symbols+' 投机 '+s.company_spec_ratio+'、套保 '+s.company_hedge_ratio).join('；')||'表内未单列特殊月份')+'。不由保证金推导最低开仓。',
    '完整性：已解析来源中的品种标准和逐月份专项，非挂牌全集；最后取得 '+timeText(status.broker_margin_readback.received_at)+'；源SHA256 '+status.broker_margin_readback.source_sha256
  ]);
  if(constraintEvidence) {
    const dated=constraintEvidence.historical_explicit_minimum;
    const older=dated.products[p.key]||[];
    const current=(status.user_data_gate_facts?.current_contracts||[]).filter(r=>r.product_key===p.key);
    if(current.length||older.length) {
      const section=detailSection('当前业务参数与明确开仓字段补证',[
        '当前保证金对应交易日 '+status.user_data_gate_facts.current_trade_day+'，交易所原表与公司公开标准情景分开，不是本账户实收；实际最低开仓为补充信息，以已审核依据为准，不阻断v2回测。',
        '本轮另核226项索引、86份公告正文及现行规则，保留104份新原件。通用1手不直接填入实际最低开仓；专项证据不足保持待补证。',
        '下方历史直接字段仅为 '+dated.observation_day+' 的供应商原始快照，原件接收 '+timeText(dated.received_at)+'；发布/生效时间未证，不冒充当前参数。普通最小报单与最小开仓分别显示。'
      ]);
      if(status.constraint_refresh)append(section,'p','11月17日单次刷新结果：未取得完整响应；完整正文 '+(status.constraint_refresh.body_complete?'是':'否')+'，收到 '+status.constraint_refresh.bytes+' 字节；未从残缺响应或旧日快照补造当前最低开仓。');
      if(current.length) {
        const list=append(section,'details',undefined,'contract-list parameter-list');append(list,'summary','查看当前 '+current.length+' 个具体合约的保证金和已审核最低开仓');
        for(const r of current)append(list,'p',r.symbol+'：投机多/空 '+r.margin.long+' / '+r.margin.short+'（比例；'+(r.margin.basis==='company'?'五矿公司统一标准情景':'交易所原表')+'）；实际最低开仓 '+(r.effective_min_open_lots===null?'待补证':r.effective_min_open_lots+'手')+'；依据 '+(r.minimum_evidence_ids.join('、')||'未知'));
      }
      if(older.length) {
        const list=append(section,'details',undefined,'contract-list parameter-list historical-parameter-list');append(list,'summary','查看 '+dated.observation_day+' 明确字段 '+older.length+' 个合约（历史观察，未升级当前门槛）');
        for(const r of older)append(list,'p',r.symbol+'（源键 '+r.source_native+'）：最小限价开仓 '+r.limit_min_open+'手；最小市价开仓 '+(r.market_min_open>0?r.market_min_open+'手':'未知/不可用')+'；普通最小限价报单 '+(r.ordinary_limit_min>0?r.ordinary_limit_min+'手':'未知/不可用')+'。');
      }
    }
  }
  if(p.user_data_gates) {
    const g=p.user_data_gates;
    const listing=status.user_data_gate_facts.listing_days?.[p.key];
    if(listing)detailSection('上市起点的独立依据（不认证全年日历）',[
      '明确上市日 '+listing.day+'；'+(listing.publisher||'已核官方上市通知')+'；置信度 '+(listing.confidence==='medium-high'?'中高（具名刊载/转录，非原站原件认证）':'高（仅上市日期事实）')+'。',
      listing.limits||'仅绑定原文明示上市日起点；不将规则生效日、首根行情或合约到期日当上市日。',
      ...(listing.evidence||[]).map(r=>'来源 '+r.url+'；SHA256 '+r.sha256)
    ]);
    detailSection('用户指定的两道数据门槛',[
      '回测：'+gateLabel(g.backtest.status)+'；当前适用合约 '+(g.backtest.eligible_contracts.join('、')||'尚无完整交集')+'。',
      ...Object.entries(g.backtest.hard_items).map(([name,v])=>gateItemLabel(name)+'：'+gateLabel(v.status)+'。'+v.reason),
      '历史有效OHLC逐年观察日期（各来源最大值，不相加）：'+Object.entries(g.backtest.hard_items.history_ohlc.coverage.valid_days_by_year).map(([year,count])=>year+'年'+count+'日').join('、')+'；缺日和版本待核仍保留。',
      '实际最低开仓（补充项，不阻断回测）：'+gateLabel(g.supplementary.effective_min_open.status)+'。'+g.supplementary.effective_min_open.reason,
      '模拟手数：由回测任务显式设置较大值；实际未知仍为空。不是交易所实际要求，也未自动改动引擎或账户参数。',
      '逐日信号：'+gateLabel(g.daily_signal.status)+'；目标日 '+status.user_data_gate_facts.latest_completed_day+'，'+g.daily_signal.eligible_contracts.length+'个合约。',
      ...Object.entries(g.daily_signal.hard_items).map(([name,v])=>gateItemLabel(name)+'：'+gateLabel(v.status)+'。'+v.reason),
      '日历、历史制度、PIT、生命周期、对账和运行成熟度为加分项；旧严格金融包不是这两道门槛的附加条件。当前参数回放历史属于情景，不代表历史实际制度。'
    ]);
    const readback=status.user_data_gate_readback;
    if(readback?.results.some(r=>r.product===p.key))detailSection('首批单日包消费（历史记录）',[
      '已逐件核'+readback.result_files_checked+'份结果的摘要、合约及真实结算。该单日MACD作业均为预热不足，实际就绪策略信号 '+readback.ready_signals+'；不改变上方数据门槛。',
      '首包只含目标日结算，不含保证金或最低开仓约束；该版通用假设文案已在后续本地候选修正，旧结果保留。未新增真实前向样本，未部署生产。'
    ]);
    const history=status.history_consumer_readback;
    if(history) {
      const tested=history.tested_contracts.filter(r=>r.contract.replace(/[0-9]+$/,'')===p.key);
      if(tested.length)detailSection('补充：完整结算历史后的有限实测',[
        ...tested.map(r=>r.contract+'：'+r.observations+'条实际观察；'+r.ready_days+'个历史观察日可计算指标价格条件；目标日'+(r.target_ready?'已到列就绪判定':'样本不足')+'。'),
        '当前固定CN MACD实际第607条才可就绪；这是策略窗口限制，不增加数据门槛，也不等于已产生可执行动作。',
        history.whole_bundle_queue_ready===true?'该版整包已按原冻结预算核验；仅所列有限合约实测，未认证全52连续队列或部署。':'该版整包尚未通过队列冻结；直接消费的成功与整包可入队分开。',
        '原始数值精度照留；历史字段仅结算，非长期OHLC回测包；没有真实前向新增或生产变更。'
      ]);
    }
  }
  if(p.usage_readiness) {
    const r=p.usage_readiness;
    detailSection('补充观察（不增加新硬项）',[
      '价格研究回测：'+(r.research_backtest==='partial_price_research'?'已有部分有效价格观察。':'未核到有效OHLC行。')+r.research_conditions,
      '旧严格金融包观察（非用户新数据门槛）：'+r.full_backtest_gaps,
      r.latest_official?'最新官方日表 '+r.latest_official.day+'，基本有效OHLC '+(r.latest_official.valid_rows??'未知')+'/'+r.latest_official.rows+'条，取得 '+timeText(r.latest_official.acquired_at)+'。':'本表没有当前官方日表的完整取得事实。',
      '盘前持续交付：'+r.preopen_conditions
    ]);
  }
  if(p.extension_availability)detailSection('本轮历史日线补充（旧封存不改写）',[
    '新增非空 '+(p.extension_availability.nonempty||0)+' 份；完整空表头 '+(p.extension_availability.empty_unavailable||0)+' 份；空数据不计成功或覆盖。',
    p.extension_quality?'相交 '+p.extension_quality.overlapping_days+' 合约日；'+p.extension_quality.distinct_fields+'字段数值不同、'+p.extension_quality.missing_official_days+'个官方观察日期未出现在本来源。完整差异留私有研究包，未自动修复。':'无新增非空样本；不据此填数据来源。'
  ]);
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
  detailSection('补充边界',['官方历史合约全集及逐合约挂牌/摘牌仍未证；与用户新两门槛独立展示，旧严格金融包协议和生产任务未改。','供应商中文简称：'+p.vendor_name+'；显示别名不作为官方规则证据。']);
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

if(status.research_union){const u=status.research_union;$('inventory-scope').textContent+=' EDB研究路线并集：'+u.states.nonempty+'份非空、'+u.states.empty_unavailable+'份完整空、'+u.states.not_requested_on_directory_route+'项未请求，'+number(u.rows)+'行；其他来源不相加。';}
if(status.project_inventory?.history_quality)$('difference-note').textContent+=' 本轮新路线另有 '+status.project_inventory.history_quality.new_distinct_fields+' 个数值差异字段待核。';
