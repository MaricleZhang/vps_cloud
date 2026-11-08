import { useMemo, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { QRCodeSVG } from 'qrcode.react';
import { Activity, ArrowDown, ArrowRight, ArrowUp, Bell, BookOpen, CalendarDays, Check, CheckCircle2, ChevronLeft, ChevronRight, Clock3, Copy, CreditCard, Download, Gauge, Globe2, HelpCircle, Laptop, MessageCircle, Monitor, Plus, QrCode, RefreshCw, Search, Send, ShieldCheck, Smartphone, Sparkles, Ticket as TicketIcon, Users, Wallet, Wifi } from 'lucide-react';
import { api, useResource, money, date, gb, trafficGB, subscriptionGB, serviceReason } from './lib';
import type { User, Plan, Node as CloudNode, Subscription, Order, Article, TrafficRecord, Ticket, Commission, Withdrawal } from './types';
import { Hero, Card, Stat, Empty, Modal, Status, Spinner, useToast, useAuth } from './ui';

type DashboardData = { user: User; subscription: Subscription | null; announcements: Article[]; traffic: TrafficRecord[]; subscription_url: string };
type InviteData = { code: string; invite_url: string; registered: number; balance: number; pending: number; rate: number; records: Commission[]; withdrawals: Withdrawal[] };

const periods: Record<string, string> = { monthly: '月付', quarterly: '季付', yearly: '年付', onetime: '一次性' };
const priorityNames: Record<string, string> = { low: '一般', normal: '普通', high: '紧急' };

function asArray<T>(value: T[] | null | undefined): T[] { return Array.isArray(value) ? value : []; }
function errorText(error: unknown) { return error instanceof Error ? error.message : '操作失败，请重试'; }
function useAction() {
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const act = async (fn: () => Promise<unknown>, success?: string) => {
    if (busy) return false;
    setBusy(true);
    try { await fn(); if (success) toast(success); return true; }
    catch (error) { toast(errorText(error), 'error'); return false; }
    finally { setBusy(false); }
  };
  return { busy, act };
}

function Loading({ loading, error, retry }: { loading: boolean; error: string; retry: () => unknown }) {
  if (loading) return <Card><Spinner /></Card>;
  if (error) return <Card><Empty title="暂时无法加载" description={error} action={<button className="button button-primary" onClick={() => void retry()}><RefreshCw size={16} />重新加载</button>} /></Card>;
  return null;
}

async function copyText(text: string) {
  if (!text) throw new Error('当前没有可复制的链接');
  if (!navigator.clipboard?.writeText) throw new Error('当前浏览器无法自动复制，请选中链接手动复制');
  try { await navigator.clipboard.writeText(text); }
  catch { throw new Error('复制未成功，请选中链接手动复制'); }
}

function Info({ label, children }: { label: string; children: ReactNode }) {
  return <div><span className="muted small">{label}</span><div className="metric-inline">{children}</div></div>;
}

function Dashboard() {
  const resource = useResource<DashboardData>('/dashboard');
  const { data, loading, error, reload } = resource;
  const { busy, act } = useAction();
  const toast = useToast();
  const [platform, setPlatform] = useState('windows');
  const [qr, setQr] = useState(false);
  const [reset, setReset] = useState(false);
  const sub = data?.subscription;
  const url = data?.subscription_url || '';
  const trafficUsed = sub ? subscriptionGB(sub, 'used') : 0;
  const trafficLimit = sub ? subscriptionGB(sub, 'limit') : 0;
  const used = trafficLimit > 0 ? Math.min(100, Math.max(0, trafficUsed / trafficLimit * 100)) : 0;
  const reason = sub?.service_reason || (sub && sub.status !== 'active' ? sub.status : sub && trafficUsed >= trafficLimit ? 'quota_exceeded' : 'active');
  const doCopy = () => { void copyText(url).then(() => toast('订阅地址已复制，请妥善保管')).catch(error => toast(errorText(error), 'error')); };
  const resetLink = async () => { if (await act(async () => { await api('/subscription/reset', { method: 'POST' }); await reload(); }, '已生成新订阅地址，旧链接已失效')) setReset(false); };
  const importClient = () => {
    if (!url || platform === 'ios') return;
    window.location.href = `clash://install-config?url=${encodeURIComponent(url)}&name=Nice%20Cloud`;
  };
  return <div className="page-stack">
    <Hero title={`你好，${data?.user.name || '欢迎回来'}`} description="查看当前服务状态、流量使用情况和账户概览。"><Link className="button hero-button" to="/plans">购买套餐<ArrowRight size={16} /></Link></Hero>
    <Loading loading={loading} error={error} retry={reload} />
    {data && <>
      {asArray(data.announcements).map(article => <div className="notice" key={article.id}><Bell size={19} /><div><strong>{article.title}</strong><p>{article.body}</p></div></div>)}
      {sub && sub.service_enabled !== true && reason !== 'active' && <div className="notice" role="status"><ShieldCheck size={19} /><div><strong>{serviceReason(reason)}</strong><p>{reason === 'quota_exceeded' ? '当前周期额度已用尽，节点同步策略后将停止服务。周期套餐将在下次重置后恢复额度，也可联系管理员或购买套餐。' : '服务暂不可用，请查看订阅有效期与账户状态，或联系管理员。'}</p></div></div>}
      <div className="stats-grid">
        <Stat label="当前订阅" value={sub?.plan_name || '暂未订阅'} icon={<Wifi />} tone="gradient" />
        <Stat label="剩余流量" value={sub ? gb(Math.max(0, trafficLimit - trafficUsed)) : '—'} icon={<Activity />} tone="green" />
        <Stat label="订阅到期" value={sub ? date(sub.expires_at) : '—'} icon={<CalendarDays />} tone="blue" />
        <Stat label="账户余额" value={money(data.user.balance)} icon={<Wallet />} tone="orange" />
      </div>
      {sub ? <div className="grid-2 dashboard-columns">
        <Card className="subscription-card"><div className="card-header"><h2>我的订阅</h2><Status value={reason === 'active' ? sub.status : reason} /></div><div className="subscription-title"><span className="subscription-icon"><Wifi size={28} /></span><div><h3>{sub.plan_name}</h3><p className="muted small">有效期至 {date(sub.expires_at)}</p></div></div><div className="field-row"><span className="muted">已用流量</span><strong>{used.toFixed(1)}%</strong></div><div className="progress-track"><div className="progress-fill" style={{ width: `${used}%` }} /></div><p className="muted small">已用 {gb(trafficUsed)} / 总计 {gb(trafficLimit)}</p><div className="detail-grid"><Info label="设备数量"><Users size={17} />{sub.device_limit > 0 ? `${sub.device_limit} 台` : '不限'}</Info><Info label="速度上限"><Gauge size={17} />{sub.speed_mbps > 0 ? `${sub.speed_mbps} Mbps` : '不限速'}</Info></div><Link className="button button-ghost full-width" to="/plans">查看全部套餐<ArrowRight size={16} /></Link></Card>
        <Card><div className="card-header"><div><h2>快捷导入订阅信息</h2><p className="muted small">选择设备类型，复制或导入当前订阅地址。</p></div></div><div className="actions"><button className="button button-primary" onClick={doCopy} disabled={!url}><Copy size={16} />复制订阅地址</button><button className="button button-ghost" onClick={() => setQr(true)} disabled={!url}><QrCode size={17} />扫码导入</button></div><div className="platform-options">{[{ id: 'ios', label: 'iOS', icon: <Smartphone size={19} /> }, { id: 'android', label: 'Android', icon: <Smartphone size={19} /> }, { id: 'macos', label: 'macOS', icon: <Laptop size={19} /> }, { id: 'windows', label: 'Windows', icon: <Monitor size={19} /> }].map(item => <button key={item.id} onClick={() => setPlatform(item.id)} className={`platform-option ${platform === item.id ? 'active' : ''}`} aria-pressed={platform === item.id}>{item.icon}{item.label}</button>)}</div><button className="button button-ghost full-width" onClick={importClient} disabled={!url || platform === 'ios'}><Download size={17} />{platform === 'ios' ? 'Shadowrocket 适配尚未完成' : '快捷导入 Clash'}<ArrowRight size={16} /></button><p className="muted small">当前输出为 Clash YAML 配置，仅支持 Clash 系客户端。需先安装客户端；无法唤起时可复制链接导入。<Link to="/docs">查看教程</Link></p></Card>
      </div> : <Card><Empty title="还没有订阅套餐" description="选择适合你的流量与设备额度，开启服务。" action={<Link className="button button-primary" to="/plans">浏览套餐<ArrowRight size={16} /></Link>} /></Card>}
      <Card><div className="card-header"><div><h2>订阅信息</h2><p className="muted small">查看当前服务与订阅安全设置。</p></div><Link to="/traffic" className="button button-ghost"><Activity size={16} />流量明细</Link></div><div className="detail-grid"><Info label="服务状态">{sub ? <Status value={reason === 'active' ? sub.status : reason} /> : '暂无订阅'}</Info><Info label="下次流量重置">{sub?.reset_at ? date(sub.reset_at) : '—'}</Info><Info label="账户状态"><Status value={data.user.status} /></Info></div>{sub && <div className="field-row subscription-reset"><div><strong>重置订阅</strong><p className="muted small">重新生成订阅链接，旧链接将失效。</p></div><button className="button button-ghost" onClick={() => setReset(true)}><RefreshCw size={16} />重置订阅</button></div>}</Card>
    </>}
    {qr && <Modal title="扫码导入订阅" onClose={() => setQr(false)}><div className="qr-panel"><QRCodeSVG value={url} size={208} level="M" /><input aria-label="订阅地址（可手动复制）" readOnly value={url} onFocus={event => event.currentTarget.select()} /><p className="muted small">请使用支持 Clash YAML 的客户端扫码，勿将二维码分享给他人。</p><button className="button button-primary" onClick={doCopy}><Copy size={16} />复制订阅地址</button></div></Modal>}
    {reset && <Modal title="确认重置订阅链接" onClose={() => !busy && setReset(false)}><p>旧链接及二维码将立即失效，所有设备都需要重新导入新订阅。</p><div className="actions"><button className="button button-ghost" disabled={busy} onClick={() => setReset(false)}>取消</button><button className="button button-danger" disabled={busy} onClick={() => void resetLink()}>{busy ? '正在重置…' : '确认重置'}</button></div></Modal>}
  </div>;
}

function PlanFeatures({ plan }: { plan: Plan }) {
  return <><div className="detail-grid"><Info label="套餐流量">{gb(plan.traffic_gb)}</Info><Info label="设备数量">{plan.device_limit > 0 ? `${plan.device_limit} 台` : '不限'}</Info><Info label="速度上限">{plan.speed_mbps > 0 ? `${plan.speed_mbps} Mbps` : '不限速'}</Info></div><ul>{(plan.description || '').split('\n').filter(Boolean).map((line, index) => <li key={index}><Check size={15} /><span>{line}</span></li>)}</ul></>;
}

function Plans() {
  const { data, loading, error, reload } = useResource<Plan[]>('/plans');
  const plans = asArray(data);
  const [selected, setSelected] = useState<Plan | null>(null);
  const [period, setPeriod] = useState('monthly');
  const { busy, act } = useAction();
  const navigate = useNavigate();
  const { user } = useAuth();
  const price = (plan: Plan, key: string) => key === 'onetime' ? plan.onetime_price : key === 'quarterly' ? plan.quarterly_price : key === 'yearly' ? plan.yearly_price : plan.monthly_price;
  const options = (plan: Plan) => plan.kind === 'onetime' ? ['onetime'] : ['monthly', 'quarterly', 'yearly'].filter(key => price(plan, key) > 0);
  const choose = (plan: Plan) => { setSelected(plan); setPeriod(options(plan)[0] || 'monthly'); };
  const buy = async () => { if (!selected) return; await act(async () => { const order = await api<Order>('/orders', { method: 'POST', body: JSON.stringify({ plan_id: selected.id, period }) }); navigate(`/orders?order=${order.id}`); }, '订单已创建，请核对并支付'); };
  return <div className="page-stack"><Hero title="选择适合你的服务方案" description="根据使用需求、有效期和流量额度，找到刚好适合的套餐。" /><Loading loading={loading} error={error} retry={reload} />{!loading && !error && <>
    <div className="grid-3 plans-grid">{plans.filter(plan => plan.active).map(plan => <Card key={plan.id} className={`plan-card ${plan.recommended ? 'recommended' : ''}`}>{plan.recommended && <span className="plan-badge"><Sparkles size={13} />推荐</span>}<div className="card-header"><h2>{plan.name}</h2><span className="plan-mark"><Wifi size={22} /></span></div><div className="plan-price">{money(plan.kind === 'onetime' ? plan.onetime_price : plan.monthly_price)}<small>/ {plan.kind === 'onetime' ? '一次性' : '月付'}</small></div><PlanFeatures plan={plan} /><button className={`button ${plan.recommended ? 'button-primary' : 'button-ghost'} full-width`} onClick={() => choose(plan)}>立即购买<ArrowRight size={16} /></button></Card>)}</div>
    {!plans.some(plan => plan.active) && <Card><Empty title="套餐正在准备中" description="暂时没有上架的服务方案，请稍后再来。" /></Card>}
    <div className="notice"><HelpCircle size={18} /><p>不同套餐的有效期和使用条件可能不同，请在确认订单前仔细核对。当前支持账户余额结算；第三方支付尚未接入。</p></div>
  </>}{selected && <Modal title="确认服务方案" onClose={() => !busy && setSelected(null)}><h3>{selected.name}</h3><PlanFeatures plan={selected} /><div className="form-field"><label htmlFor="plan-period">付款周期</label><select id="plan-period" value={period} onChange={event => setPeriod(event.target.value)}>{options(selected).map(key => <option key={key} value={key}>{periods[key]} · {money(price(selected, key))}</option>)}</select></div><div className="detail-grid"><Info label="订单金额">{money(price(selected, period))}</Info><Info label="当前余额">{money(user?.balance || 0)}</Info></div><p className="muted small">创建订单不会立即扣款。同套餐续费顺延到期并保留本周期用量；更换套餐会立即替换原套餐，剩余时长和流量不折抵。</p><div className="actions"><button className="button button-ghost" disabled={busy} onClick={() => setSelected(null)}>取消</button><button className="button button-primary" disabled={busy || options(selected).length === 0} onClick={() => void buy()}>{busy ? '正在创建…' : '创建订单'}<ArrowRight size={16} /></button></div></Modal>}</div>;
}

function Nodes() {
  const { data, loading, error, reload } = useResource<CloudNode[]>('/nodes');
  const [search, setSearch] = useState('');
  const [region, setRegion] = useState('all');
  const nodes = asArray(data);
  const regions = [...new Set(nodes.map(node => node.region))];
  const filtered = nodes.filter(node => (region === 'all' || node.region === region) && `${node.name} ${node.region} ${node.protocol}`.toLowerCase().includes(search.toLowerCase()));
  const online = nodes.filter(node => node.status === 'online').length;
  return <div className="page-stack"><Hero title="节点列表" description="查看服务节点的可用状态与线路信息。" /><Loading loading={loading} error={error} retry={reload} />{!loading && !error && <>{nodes.some(node => node.status === 'demo') && <div className="notice"><Globe2 size={18} /><p>标为演示的节点仅用于展示界面，未接入真实网络；不会计入在线节点，也不会生成可连接配置。</p></div>}<Card><div className="field-row"><p><strong>{online}</strong> / {nodes.length} 个节点在线 <span className="muted small">状态以服务端检测为准</span></p><div className="search-field"><Search size={17} /><input aria-label="搜索节点" placeholder="搜索节点或地区" value={search} onChange={event => setSearch(event.target.value)} /></div></div><div className="tabs"><button className={`tab ${region === 'all' ? 'active' : ''}`} onClick={() => setRegion('all')} aria-pressed={region === 'all'}>全部地区</button>{regions.map(item => <button key={item} className={`tab ${region === item ? 'active' : ''}`} onClick={() => setRegion(item)} aria-pressed={region === item}>{item}</button>)}</div></Card><div className="grid-3 node-grid">{filtered.map(node => <Card key={node.id} className="node-card"><div className="card-header"><span className="node-flag">{node.flag || <Globe2 size={25} />}</span><Status value={node.status} /></div><h3>{node.name}</h3><span className="tag">{(node.protocol || '未配置').toUpperCase()}</span><div className="detail-grid"><Info label="流量倍率">{node.multiplier}×</Info><Info label="地区">{node.region}</Info></div><div className="field-row"><span className="muted small">{node.tags || '标准线路'}</span><span className="muted small">{date(node.updated_at)}</span></div></Card>)}</div>{filtered.length === 0 && <Card><Empty title={nodes.length ? '没有找到匹配节点' : '暂无可用节点'} description={nodes.length ? '尝试其他名称或地区。' : '节点接入完成后将在这里显示。'} /></Card>}</>}</div>;
}

function Orders() {
  const { data, loading, error, reload } = useResource<Order[]>('/orders');
  const allOrders = asArray(data);
  const { user, refresh } = useAuth();
  const [filter, setFilter] = useState('all');
  const [params, setParams] = useSearchParams();
  const [confirm, setConfirm] = useState<'pay' | 'cancel' | null>(null);
  const { busy, act } = useAction();
  const selected = allOrders.find(order => String(order.id) === params.get('order'));
  const orders = allOrders.filter(order => filter === 'all' || (filter === 'paid' ? ['paid', 'completed'].includes(order.status) : order.status === filter));
  const paid = allOrders.filter(order => order.status === 'paid' || order.status === 'completed');
  const open = allOrders.filter(order => order.status === 'pending');
  const select = (id?: number) => { setConfirm(null); setParams(id ? { order: String(id) } : {}); };
  const mutate = async (action: 'pay' | 'cancel') => { if (!selected) return; if (await act(async () => { await api(`/orders/${selected.id}/${action}`, { method: 'POST' }); await Promise.all([reload(), refresh()]); }, action === 'pay' ? '支付成功，订阅权益已生效' : '订单已取消')) setConfirm(null); };
  return <div className="page-stack"><Hero title="我的订单" description="购买记录、支付状态和套餐信息都在这里。"><Link className="button hero-button" to="/plans">查看方案<ArrowRight size={16} /></Link></Hero><Loading loading={loading} error={error} retry={reload} />{!loading && !error && <><div className="stats-grid"><Stat label="累计订单" value={allOrders.length} icon={<CreditCard />} tone="gradient" /><Stat label="已支付金额" value={money(paid.reduce((sum, order) => sum + order.amount, 0))} icon={<Wallet />} tone="green" /><Stat label="待支付订单" value={open.length} icon={<Clock3 />} tone="orange" /><Stat label="已完成订单" value={paid.length} icon={<CheckCircle2 />} tone="blue" /></div><Card><div className="tabs">{[{ id: 'all', name: '全部订单' }, { id: 'pending', name: '待支付' }, { id: 'paid', name: '已支付' }, { id: 'cancelled', name: '已取消' }].map(item => <button key={item.id} className={`tab ${filter === item.id ? 'active' : ''}`} onClick={() => setFilter(item.id)} aria-pressed={filter === item.id}>{item.name}</button>)}</div>{orders.length ? <div className="table-wrap"><table className="data-table"><thead><tr><th>订单</th><th>周期</th><th>金额</th><th>创建时间</th><th>状态</th><th>操作</th></tr></thead><tbody>{orders.map(order => <tr key={order.id}><td><strong>{order.plan_name}</strong><div className="muted small">{order.order_no}</div></td><td>{periods[order.period] || order.period}</td><td className="amount">{money(order.amount)}</td><td>{date(order.created_at)}</td><td><Status value={order.status === 'pending' ? 'pending_payment' : order.status} /></td><td><button className="button button-ghost button-small" onClick={() => select(order.id)}>付款详情<ChevronRight size={14} /></button></td></tr>)}</tbody></table></div> : <Empty title="暂无订单" description="选择服务方案后，订单会显示在这里。" action={<Link className="button button-primary" to="/plans">浏览套餐</Link>} />}</Card></>}{selected && <Modal title="订单详情" onClose={() => !busy && select()}><div className="field-row"><h3>{selected.plan_name}</h3><Status value={selected.status === 'pending' ? 'pending_payment' : selected.status} /></div><div className="detail-grid"><Info label="订单编号">{selected.order_no}</Info><Info label="付款周期">{periods[selected.period] || selected.period}</Info><Info label="订单金额">{money(selected.amount)}</Info><Info label="创建时间">{date(selected.created_at)}</Info>{selected.paid_at && <Info label="支付时间">{date(selected.paid_at)}</Info>}<Info label="账户余额">{money(user?.balance || 0)}</Info></div>{selected.status === 'pending' && <><div className="notice"><ShieldCheck size={18} /><p>本次使用账户余额结算。第三方支付尚未接入，请联系管理员处理充值。</p></div>{confirm ? <div className="notice"><div><strong>{confirm === 'pay' ? `确认从余额扣除 ${money(selected.amount)}？` : '确认取消此订单？'}</strong><p className="small">{confirm === 'pay' ? '同套餐续费顺延到期；更换套餐将替换原套餐，剩余时长和流量不折抵。' : '取消后该订单将无法继续支付。'}</p><div className="actions"><button className="button button-ghost" disabled={busy} onClick={() => setConfirm(null)}>返回</button><button className={`button ${confirm === 'pay' ? 'button-primary' : 'button-danger'}`} disabled={busy} onClick={() => void mutate(confirm)}>{busy ? '正在处理…' : '确认'}</button></div></div></div> : <div className="actions"><button className="button button-ghost" onClick={() => setConfirm('cancel')}>取消订单</button><button className="button button-primary" onClick={() => setConfirm('pay')} disabled={(user?.balance || 0) < selected.amount}><Wallet size={16} />{(user?.balance || 0) < selected.amount ? '余额不足' : '余额支付'}</button></div>}</>}</Modal>}</div>;
}

function Invites() {
  const { data, loading, error, reload } = useResource<InviteData>('/invites');
  const { busy, act } = useAction();
  const toast = useToast();
  const [withdraw, setWithdraw] = useState(false);
  const [amount, setAmount] = useState('');
  const [account, setAccount] = useState('');
  const generate = () => void act(async () => { await api('/invites/code', { method: 'POST' }); await reload(); }, '邀请码已生成');
  const copy = () => void copyText(data?.invite_url || data?.code || '').then(() => toast('邀请链接已复制')).catch(error => toast(errorText(error), 'error'));
  const submit = async (event: FormEvent) => { event.preventDefault(); const cents = Math.round(Number(amount) * 100); if (!Number.isFinite(cents) || cents <= 0 || cents > (data?.balance || 0)) { toast('请输入佣金余额范围内的提现金额', 'error'); return; } if (await act(async () => { await api('/withdrawals', { method: 'POST', body: JSON.stringify({ amount: cents, account: account.trim() }) }); await reload(); }, '提现申请已提交，等待管理员审核')) { setWithdraw(false); setAmount(''); setAccount(''); } };
  return <div className="page-stack"><Hero title="我的邀请" description="分享服务，查看邀请记录与佣金结算。"><div className="actions"><button className="button hero-button" disabled={busy} onClick={generate}><Plus size={16} />{data?.code ? '获取邀请码' : '生成邀请码'}</button><button className="button hero-button" disabled={!data || data.balance <= 0} onClick={() => setWithdraw(true)}><Wallet size={16} />佣金提现</button></div></Hero><Loading loading={loading} error={error} retry={reload} />{data && <><div className="stats-grid"><Stat label="已注册用户" value={data.registered} icon={<Users />} tone="gradient" /><Stat label="佣金余额" value={money(data.balance)} icon={<Wallet />} tone="green" /><Stat label="提现处理中" value={money(data.pending)} icon={<Clock3 />} tone="orange" /><Stat label="返佣比例" value={`${data.rate > 1 ? data.rate : data.rate * 100}%`} icon={<Sparkles />} tone="blue" /></div><Card>{data.code ? <><div className="card-header"><div><h2>专属邀请链接</h2><p className="muted small">通过此链接注册的用户将关联你的邀请关系。</p></div></div><div className="copy-field"><input readOnly aria-label="邀请链接" value={data.invite_url || data.code} /><button className="button button-primary" onClick={copy}><Copy size={16} />复制链接</button></div><p className="muted small">邀请码：{data.code}</p></> : <Empty title="还没有邀请码" description="生成专属邀请码，开始邀请朋友。" action={<button className="button button-primary" onClick={generate} disabled={busy}><Plus size={16} />生成邀请码</button>} />}</Card><Card><div className="card-header"><div><h2>佣金明细</h2><p className="muted small">订单结算后显示对应佣金记录。</p></div></div>{asArray(data.records).length ? <div className="table-wrap"><table className="data-table"><thead><tr><th>关联订单</th><th>佣金</th><th>状态</th><th>时间</th></tr></thead><tbody>{asArray(data.records).map(record => <tr key={record.id}><td>{record.order_no}</td><td>{money(record.amount)}</td><td><Status value={record.status} /></td><td>{date(record.created_at)}</td></tr>)}</tbody></table></div> : <Empty title="暂无佣金记录" description="邀请用户完成符合条件的订单后，佣金将显示在这里。" />}</Card><Card><h2>提现记录</h2>{asArray(data.withdrawals).length ? <div className="table-wrap"><table className="data-table"><thead><tr><th>申请金额</th><th>收款信息</th><th>状态</th><th>申请时间</th></tr></thead><tbody>{asArray(data.withdrawals).map(record => <tr key={record.id}><td>{money(record.amount)}</td><td>{record.account}</td><td><Status value={record.status} /></td><td>{date(record.created_at)}</td></tr>)}</tbody></table></div> : <Empty title="暂无提现申请" description="可提现佣金余额大于零时，可以提交提现申请。" />}</Card></>}{withdraw && <Modal title="申请佣金提现" onClose={() => !busy && setWithdraw(false)}><form onSubmit={event => void submit(event)} className="page-stack"><p className="muted">可提现余额 <strong>{money(data?.balance || 0)}</strong>，申请后金额将冻结至审核完成。</p><div className="form-field"><label htmlFor="withdraw-amount">提现金额（元）</label><input id="withdraw-amount" type="number" min="0.01" step="0.01" max={(data?.balance || 0) / 100} required value={amount} onChange={event => setAmount(event.target.value)} /></div><div className="form-field"><label htmlFor="withdraw-account">收款信息</label><textarea id="withdraw-account" placeholder="请填写收款方式和账号，供管理员审核" required maxLength={500} value={account} onChange={event => setAccount(event.target.value)} /></div><div className="actions"><button type="button" className="button button-ghost" disabled={busy} onClick={() => setWithdraw(false)}>取消</button><button className="button button-primary" disabled={busy}>{busy ? '正在提交…' : '提交申请'}</button></div></form></Modal>}</div>;
}

function Account() {
  const { user, refresh } = useAuth();
  const { data, loading, error, reload } = useResource<{ subscription: Subscription | null; url: string }>('/subscription');
  const { busy, act } = useAction();
  const toast = useToast();
  const [current, setCurrent] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const submit = async (event: FormEvent) => { event.preventDefault(); if (password !== confirm) { toast('两次输入的新密码不一致', 'error'); return; } await act(async () => { await api('/auth/password', { method: 'POST', body: JSON.stringify({ current_password: current, new_password: password }) }); setCurrent(''); setPassword(''); setConfirm(''); await refresh(); }, '密码已修改'); };
  return <div className="page-stack"><Hero title="个人中心" description="查看账户资料、服务状态和常用安全设置。" /><Loading loading={loading} error={error} retry={reload} /><div className="grid-2"><Card><div className="account-profile"><span className="avatar avatar-large">{(user?.name || user?.email || 'NC').slice(0, 2).toUpperCase()}</span><h2>{user?.name}</h2><p className="muted">{user?.email}</p><p className="muted small">加入于 {date(user?.created_at || '')}</p></div><div className="detail-grid"><Info label="账户余额">{money(user?.balance || 0)}</Info><Info label="套餐名称">{data?.subscription?.plan_name || '暂无订阅'}</Info><Info label="到期时间">{data?.subscription ? date(data.subscription.expires_at) : '—'}</Info><Info label="账户状态"><Status value={user?.status || 'active'} /></Info></div></Card><Card><div className="card-header"><div><h2>安全设置</h2><p className="muted small">定期更换密码，保护你的账户与订阅。</p></div><ShieldCheck size={22} /></div><form onSubmit={event => void submit(event)} className="page-stack"><div className="form-field"><label htmlFor="current-password">当前密码</label><input id="current-password" type="password" autoComplete="current-password" required value={current} onChange={event => setCurrent(event.target.value)} /></div><div className="form-field"><label htmlFor="new-password">新密码</label><input id="new-password" type="password" autoComplete="new-password" required minLength={10} maxLength={128} placeholder="至少 10 位字符" value={password} onChange={event => setPassword(event.target.value)} /></div><div className="form-field"><label htmlFor="confirm-password">确认新密码</label><input id="confirm-password" type="password" autoComplete="new-password" required minLength={10} value={confirm} onChange={event => setConfirm(event.target.value)} /></div><button className="button button-primary" disabled={busy}>{busy ? '正在修改…' : '修改密码'}<ShieldCheck size={16} /></button></form></Card></div></div>;
}

function Docs() {
  const { data, loading, error, reload } = useResource<Article[]>('/articles');
  const [search, setSearch] = useState('');
  const [category, setCategory] = useState('all');
  const [selected, setSelected] = useState<Article | null>(null);
  const docs = asArray(data).filter(article => article.kind === 'doc' && article.published);
  const categories = [...new Set(docs.map(article => article.category))];
  const filtered = docs.filter(article => (category === 'all' || article.category === category) && `${article.title} ${article.body}`.toLowerCase().includes(search.toLowerCase()));
  return <div className="page-stack"><Hero title="使用文档" description="从安装到导入，快速找到你需要的使用指南。" /><Loading loading={loading} error={error} retry={reload} />{!loading && !error && <><Card><div className="search-field"><Search size={18} /><input aria-label="搜索使用文档" placeholder="搜索使用说明、配置指引与常见问题" value={search} onChange={event => setSearch(event.target.value)} /></div><div className="tabs"><button className={`tab ${category === 'all' ? 'active' : ''}`} onClick={() => setCategory('all')} aria-pressed={category === 'all'}>全部文档</button>{categories.map(item => <button className={`tab ${category === item ? 'active' : ''}`} key={item} onClick={() => setCategory(item)} aria-pressed={category === item}>{item}</button>)}</div></Card><div className="grid-2">{filtered.map(article => <Card key={article.id}><div className="card-header"><span className="doc-icon"><BookOpen size={21} /></span><span className="tag">{article.category}</span></div><h2>{article.title}</h2><p className="muted doc-preview">{article.body.slice(0, 110)}{article.body.length > 110 ? '…' : ''}</p><div className="field-row"><span className="muted small">更新于 {date(article.updated_at)}</span><button className="button button-ghost" onClick={() => setSelected(article)}>查看详情<ArrowRight size={16} /></button></div></Card>)}</div>{filtered.length === 0 && <Card><Empty title={search ? '没有找到相关文档' : '暂无使用文档'} description="可以尝试其他关键词，或通过工单联系我们。" action={<Link className="button button-ghost" to="/tickets"><MessageCircle size={16} />联系支持</Link>} /></Card>}</>}{selected && <Modal title={selected.title} onClose={() => setSelected(null)} wide><div className="field-row"><span className="tag">{selected.category}</span><span className="muted small">更新于 {date(selected.updated_at)}</span></div><div className="article-body" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{selected.body}</div></Modal>}</div>;
}

function TicketConversation({ id, onUpdate }: { id: number; onUpdate: () => Promise<void> }) {
  const { data, loading, error, reload } = useResource<Ticket>(`/tickets/${id}`);
  const [message, setMessage] = useState('');
  const [confirmClose, setConfirmClose] = useState(false);
  const { busy, act } = useAction();
  const send = async (event: FormEvent) => { event.preventDefault(); if (!message.trim()) return; if (await act(async () => { await api(`/tickets/${id}/messages`, { method: 'POST', body: JSON.stringify({ message: message.trim() }) }); await Promise.all([reload(), onUpdate()]); }, '回复已发送')) setMessage(''); };
  const close = async () => { if (await act(async () => { await api(`/tickets/${id}/close`, { method: 'POST' }); await Promise.all([reload(), onUpdate()]); }, '工单已关闭')) setConfirmClose(false); };
  return <><Loading loading={loading} error={error} retry={reload} />{data && <><div className="field-row"><div><span className="muted small">工单 #{data.id} · {priorityNames[data.priority] || data.priority}</span><h3>{data.subject}</h3></div><Status value={data.status} /></div><div className="message-thread">{asArray(data.messages).map(message => <div className={`message-bubble ${message.role === 'admin' ? 'admin' : 'user'}`} key={message.id}><div className="field-row small"><strong>{message.role === 'admin' ? '客服支持' : '我'}</strong><span className="muted">{date(message.created_at)}</span></div><p style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{message.message}</p></div>)}</div>{data.status !== 'closed' ? <><form onSubmit={event => void send(event)} className="page-stack"><div className="form-field"><label htmlFor="ticket-message">回复工单</label><textarea id="ticket-message" required maxLength={5000} rows={4} placeholder="请补充问题的相关信息…" value={message} onChange={event => setMessage(event.target.value)} /></div><div className="actions"><button type="button" className="button button-ghost" disabled={busy} onClick={() => setConfirmClose(true)}>关闭工单</button><button className="button button-primary" disabled={busy || !message.trim()}><Send size={16} />{busy ? '正在发送…' : '发送回复'}</button></div></form>{confirmClose && <div className="notice"><div><p>确认问题已解决并关闭工单？关闭后将无法继续回复。</p><div className="actions"><button className="button button-ghost" onClick={() => setConfirmClose(false)} disabled={busy}>取消</button><button className="button button-danger" onClick={() => void close()} disabled={busy}>确认关闭</button></div></div></div>}</> : <div className="notice"><CheckCircle2 size={18} /><p>此工单已关闭。如有新问题，请创建新的工单。</p></div>}</>}</>;
}

function Tickets() {
  const { data, loading, error, reload } = useResource<Ticket[]>('/tickets');
  const [filter, setFilter] = useState('all');
  const [create, setCreate] = useState(false);
  const [selected, setSelected] = useState<number | null>(null);
  const [subject, setSubject] = useState('');
  const [priority, setPriority] = useState('normal');
  const [message, setMessage] = useState('');
  const { busy, act } = useAction();
  const tickets = asArray(data);
  const filtered = tickets.filter(ticket => filter === 'all' || (filter === 'open' ? ticket.status !== 'closed' : ticket.status === 'closed'));
  const submit = async (event: FormEvent) => { event.preventDefault(); if (await act(async () => { const ticket = await api<Ticket>('/tickets', { method: 'POST', body: JSON.stringify({ subject: subject.trim(), priority, message: message.trim() }) }); await reload(); setSelected(ticket.id); }, '工单已创建')) { setCreate(false); setSubject(''); setMessage(''); setPriority('normal'); } };
  return <div className="page-stack"><Hero title="我的工单" description="提交问题、查看回复，解决过程不会丢失。"><button className="button hero-button" onClick={() => setCreate(true)}><Plus size={17} />新建工单</button></Hero><Loading loading={loading} error={error} retry={reload} />{!loading && !error && <><div className="stats-grid stats-three"><Stat label="全部工单" value={tickets.length} icon={<TicketIcon />} tone="gradient" /><Stat label="处理中" value={tickets.filter(ticket => ticket.status !== 'closed').length} icon={<MessageCircle />} tone="orange" /><Stat label="已关闭工单" value={tickets.filter(ticket => ticket.status === 'closed').length} icon={<CheckCircle2 />} tone="green" /></div><Card><div className="tabs">{[{ id: 'all', name: '全部工单' }, { id: 'open', name: '处理中' }, { id: 'closed', name: '已关闭' }].map(item => <button key={item.id} className={`tab ${filter === item.id ? 'active' : ''}`} onClick={() => setFilter(item.id)} aria-pressed={filter === item.id}>{item.name}</button>)}</div>{filtered.length ? <div className="table-wrap"><table className="data-table"><thead><tr><th>工单主题</th><th>优先级</th><th>状态</th><th>更新时间</th><th>操作</th></tr></thead><tbody>{filtered.map(ticket => <tr key={ticket.id}><td><strong>{ticket.subject}</strong><div className="muted small">#{ticket.id}</div></td><td>{priorityNames[ticket.priority] || ticket.priority}</td><td><Status value={ticket.status} /></td><td>{date(ticket.updated_at)}</td><td><button className="button button-ghost button-small" onClick={() => setSelected(ticket.id)}>查看工单<ChevronRight size={15} /></button></td></tr>)}</tbody></table></div> : <Empty title="还没有工单" description="遇到问题时，可以创建一张新工单。" action={<button className="button button-primary" onClick={() => setCreate(true)}><Plus size={16} />新建工单</button>} />}</Card></>}{create && <Modal title="新建工单" onClose={() => !busy && setCreate(false)}><form className="page-stack" onSubmit={event => void submit(event)}><div className="form-field"><label htmlFor="ticket-subject">问题主题</label><input id="ticket-subject" required maxLength={200} placeholder="简要描述你遇到的问题" value={subject} onChange={event => setSubject(event.target.value)} /></div><div className="form-field"><label htmlFor="ticket-priority">优先级</label><select id="ticket-priority" value={priority} onChange={event => setPriority(event.target.value)}><option value="low">一般</option><option value="normal">普通</option><option value="high">紧急</option></select></div><div className="form-field"><label htmlFor="ticket-description">详细描述</label><textarea id="ticket-description" required rows={5} maxLength={5000} placeholder="请填写设备、客户端和问题出现时间，勿提交密码或完整订阅链接。" value={message} onChange={event => setMessage(event.target.value)} /></div><div className="actions"><button type="button" className="button button-ghost" disabled={busy} onClick={() => setCreate(false)}>取消</button><button className="button button-primary" disabled={busy || !subject.trim() || !message.trim()}>{busy ? '正在提交…' : '提交工单'}<Send size={16} /></button></div></form></Modal>}{selected !== null && <Modal title="工单详情" onClose={() => setSelected(null)} wide><TicketConversation key={selected} id={selected} onUpdate={reload} /></Modal>}</div>;
}

function Traffic() {
  const { data, loading, error, reload } = useResource<TrafficRecord[]>('/traffic');
  const [page, setPage] = useState(1);
  const [month, setMonth] = useState('all');
  const records = asArray(data);
  const months = [...new Set(records.map(record => record.date.slice(0, 7)))].sort().reverse();
  const filtered = useMemo(() => asArray(data).filter(record => month === 'all' || record.date.startsWith(month)).sort((a, b) => b.date.localeCompare(a.date)), [data, month]);
  const pages = Math.max(1, Math.ceil(filtered.length / 10));
  const currentPage = Math.min(page, pages);
  const visible = filtered.slice((currentPage - 1) * 10, currentPage * 10);
  const sum = (field: 'total' | 'download' | 'upload') => filtered.reduce((total, row) => total + trafficGB(row, field), 0);
  return <div className="page-stack"><Hero title="流量明细" description="查看流量使用记录，了解每一份流量的去向。" /><Loading loading={loading} error={error} retry={reload} />{!loading && !error && <><div className="stats-grid stats-three"><Stat label="记录总用量" value={gb(sum('total'))} icon={<Activity />} tone="gradient" /><Stat label="下载流量" value={gb(sum('download'))} icon={<ArrowDown />} tone="green" /><Stat label="上传流量" value={gb(sum('upload'))} icon={<ArrowUp />} tone="blue" /></div><Card><div className="card-header"><div><h2>使用记录</h2><p className="muted small">（上传 + 下载）× 倍率 = 计费用量，按整数 bytes 计量。1 GB = 1024³ bytes，每页 10 条。</p></div><select aria-label="筛选流量月份" value={month} onChange={event => { setMonth(event.target.value); setPage(1); }}><option value="all">全部月份</option>{months.map(item => <option key={item} value={item}>{item}</option>)}</select></div>{visible.length ? <div className="table-wrap"><table className="data-table"><thead><tr><th>日期</th><th>上传</th><th>下载</th><th>流量倍率</th><th>计费用量</th><th>节点</th><th>来源</th></tr></thead><tbody>{visible.map(record => <tr key={record.id}><td>{date(record.date)}</td><td>{gb(trafficGB(record, 'upload'))}</td><td>{gb(trafficGB(record, 'download'))}</td><td>{record.multiplier.toFixed(2)}×</td><td><strong>{gb(trafficGB(record, 'total'))}</strong></td><td>{record.node_name || '—'}</td><td><span className="tag">{record.source === 'metered' ? '节点计量' : record.source === 'demo' ? '演示数据' : '历史记录'}</span></td></tr>)}</tbody></table></div> : <Empty title="暂无流量记录" description="节点上报流量后，使用明细将在这里显示。" />}<div className="field-row pagination"><span className="muted small">共 {filtered.length} 条 · 第 {currentPage} / {pages} 页</span><div className="actions"><button className="button button-ghost" disabled={currentPage <= 1} onClick={() => setPage(currentPage - 1)}><ChevronLeft size={16} />上一页</button><button className="button button-ghost" disabled={currentPage >= pages} onClick={() => setPage(currentPage + 1)}>下一页<ChevronRight size={16} /></button></div></div></Card></>}</div>;
}

export function UserPages({ page }: { page: string }) {
  switch (page.replace(/^\//, '')) {
    case 'dashboard': return <Dashboard />;
    case 'plans': return <Plans />;
    case 'nodes': return <Nodes />;
    case 'orders': return <Orders />;
    case 'invites': return <Invites />;
    case 'account': return <Account />;
    case 'docs': return <Docs />;
    case 'tickets': return <Tickets />;
    case 'traffic': return <Traffic />;
    default: return <div className="page-stack"><Hero title="页面不存在" description="这个页面可能已移动。" /><Card><Empty title="没有找到这个页面" action={<Link className="button button-primary" to="/dashboard">返回仪表盘</Link>} /></Card></div>;
  }
}
