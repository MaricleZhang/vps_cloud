import {useEffect,useState} from 'react';
import type {FormEvent} from 'react';
import {Link,NavLink,Navigate,Route,Routes,useLocation,useNavigate,useSearchParams} from 'react-router-dom';
import {Activity,ArrowLeft,ArrowRight,BarChart3,Bell,BookOpen,Check,ChevronDown,Cloud,Coins,CreditCard,Eye,EyeOff,FileText,Gauge,Gift,Globe,Headphones,LayoutDashboard,LogOut,Menu,MessageSquare,Moon,Network,Plus,Settings,Shield,ShoppingBag,SlidersHorizontal,Sun,Users,Wallet,X} from 'lucide-react';
import type {LucideIcon} from 'lucide-react';
import {api,money,useResource} from './lib';
import type {Article,SiteSettings} from './types';
import {Card,Empty,Logo,Modal,Spinner,useAuth,useToast} from './ui';
import {UserPages} from './UserPages';
import {AdminPages} from './AdminPages';

type NavItem = {path:string;label:string;icon:LucideIcon;group?:string};
const userNav:NavItem[] = [
  {path:'/dashboard',label:'仪表盘',icon:LayoutDashboard},
  {path:'/docs',label:'使用文档',icon:BookOpen},
  {path:'/nodes',label:'节点列表',icon:Globe,group:'订阅'},
  {path:'/plans',label:'购买订阅',icon:ShoppingBag},
  {path:'/orders',label:'我的订单',icon:FileText,group:'财务'},
  {path:'/invites',label:'我的邀请',icon:Gift},
  {path:'/account',label:'个人中心',icon:Users,group:'用户'},
  {path:'/tickets',label:'我的工单',icon:Headphones},
  {path:'/traffic',label:'流量明细',icon:Activity},
];
const adminNav:NavItem[] = [
  {path:'/admin/overview',label:'运营概览',icon:Gauge},
  {path:'/admin/users',label:'用户管理',icon:Users,group:'业务管理'},
  {path:'/admin/plans',label:'套餐管理',icon:ShoppingBag},
  {path:'/admin/subscriptions',label:'订阅权益',icon:CreditCard},
  {path:'/admin/nodes',label:'节点管理',icon:Network},
  {path:'/admin/orders',label:'订单管理',icon:FileText,group:'财务与统计'},
  {path:'/admin/finance',label:'支付与账务',icon:Wallet},
  {path:'/admin/traffic',label:'流量统计',icon:BarChart3},
  {path:'/admin/invites',label:'佣金与提现',icon:Coins},
  {path:'/admin/tickets',label:'工单处理',icon:MessageSquare,group:'运营与系统'},
  {path:'/admin/content',label:'公告与文档',icon:BookOpen},
  {path:'/admin/settings',label:'站点设置',icon:Settings},
  {path:'/admin/audit',label:'操作日志',icon:Shield},
];

export function App() {
  const {user,loading,error,refresh} = useAuth();
  if (loading) return <div className="boot-screen"><Logo/><Spinner/></div>;
  if (error) return <div className="boot-screen"><Logo/><Card><Empty title="暂时无法连接服务" description={error} action={<button className="button button-primary" onClick={()=>void refresh()}>重新连接</button>}/></Card></div>;
  return <Routes>
    <Route path="/login" element={user?<Navigate to={user.role==='admin'?'/admin/overview':'/dashboard'} replace/>:<AuthPage/>}/>
    <Route path="/register" element={user?<Navigate to="/dashboard" replace/>:<AuthPage register/>}/>
    <Route path="/admin/*" element={user?<Layout admin/>:<Navigate to="/login" replace/>}/>
    <Route path="/*" element={user?<Layout/>:<Navigate to="/login" replace/>}/>
  </Routes>;
}

function Layout({admin=false}:{admin?:boolean}) {
  const {user,logout} = useAuth();
  const toast = useToast();
  const location = useLocation();
  const navigate = useNavigate();
  const [drawer,setDrawer] = useState(false);
  const [mobile,setMobile] = useState(()=>window.matchMedia('(max-width:1050px)').matches);
  const [dropdown,setDropdown] = useState<'user'|'language'|null>(null);
  const [notices,setNotices] = useState(false);
  const [dark,setDark] = useState(()=>localStorage.getItem('nicecloud-theme')==='dark');
  const settings = useResource<SiteSettings>('/settings/public');
  const nav = admin?adminNav:userNav;
  const current = nav.find(item=>item.path===location.pathname);
  useEffect(()=>{const query=window.matchMedia('(max-width:1050px)');const change=()=>setMobile(query.matches);query.addEventListener('change',change);return()=>query.removeEventListener('change',change);},[]);
  useEffect(()=>{document.documentElement.dataset.theme=dark?'dark':'light';localStorage.setItem('nicecloud-theme',dark?'dark':'light');},[dark]);
  useEffect(()=>{setDrawer(false);setDropdown(null);window.scrollTo({top:0,behavior:'instant'});},[location.pathname]);
  useEffect(()=>{const close=(event:KeyboardEvent)=>{if(event.key==='Escape'){setDrawer(false);setDropdown(null);}};document.addEventListener('keydown',close);return()=>document.removeEventListener('keydown',close);},[]);
  useEffect(()=>{document.title=`${current?.label||'服务中心'} · ${settings.data?.site_name||'Nice Cloud'}`;},[current?.label,settings.data?.site_name]);
  if(admin && user?.role!=='admin') return <div className="boot-screen"><Logo/><Card><Empty title="需要管理员权限" description="当前账户没有管理端访问权限。" action={<Link className="button button-primary" to="/dashboard">返回服务中心</Link>}/></Card></div>;
  if(location.pathname==='/' || location.pathname==='/admin' || location.pathname==='/admin/') return <Navigate to={admin?'/admin/overview':'/dashboard'} replace/>;
  async function exit() {try{await logout();navigate('/login');}catch(error){toast(error instanceof Error?error.message:'退出失败','error');}}
  return <div className={`app-shell ${admin?'admin-shell':''}`}>
    <a href="#main-content" className="skip-link">跳转到主要内容</a>
    {drawer&&<button className="sidebar-shade" aria-label="关闭导航" onClick={()=>setDrawer(false)}/>}
    <aside className={`sidebar ${drawer?'is-open':''}`} aria-label={admin?'管理导航':'主导航'} inert={mobile&&!drawer} aria-hidden={mobile&&!drawer}>
      <Link className="sidebar-brand" to={admin?'/admin/overview':'/dashboard'}><Logo name={settings.data?.site_name}/></Link>
      {admin&&<span className="admin-label"><Shield size={12}/> 管理控制台</span>}
      <button className="icon-button drawer-close" aria-label="关闭导航" onClick={()=>setDrawer(false)}><X size={19}/></button>
      <nav className="nav-list">{nav.map(({path,label,icon:Icon,group})=><div key={path}>{group&&<div className="nav-group">{group}</div>}<NavLink to={path} className={({isActive})=>`nav-link ${isActive?'active':''}`}><span className="nav-icon"><Icon size={19}/></span><span>{label}</span></NavLink></div>)}</nav>
      <div className="sidebar-bottom">{user?.role==='admin'&&<Link to={admin?'/dashboard':'/admin/overview'} className="nav-link switch-console"><SlidersHorizontal size={17}/>{admin?'返回服务中心':'进入管理后台'}<ArrowRight size={15}/></Link>}<button className="nav-link logout" onClick={()=>void exit()}><LogOut size={18}/><span>退出登录</span></button><div className="sidebar-footer"><span className="tiny-dot"/>Nice Cloud · 服务中心</div></div>
    </aside>
    <div className="workspace">
      <header className="topbar">
        <div className="topbar-left"><button className="icon-button mobile-menu" aria-label="打开主导航" aria-expanded={drawer} onClick={()=>setDrawer(!drawer)}><Menu size={21}/></button><div className="desktop-breadcrumb">{admin?'管理控制台':'服务中心'}<span>/</span><strong>{current?.label||'页面'}</strong></div><Link className="mobile-brand" to="/dashboard"><Logo name={settings.data?.site_name}/></Link></div>
        <div className="topbar-actions">
          <div className="dropdown-anchor"><button className="icon-button language-button" aria-label="选择语言" aria-expanded={dropdown==='language'} onClick={()=>setDropdown(dropdown==='language'?null:'language')}><Globe size={19}/></button>{dropdown==='language'&&<div className="dropdown language-dropdown"><strong>语言 / Language</strong><div className="language-selected"><Check size={16}/>简体中文</div><p className="muted small">其他语言版本暂未提供。</p></div>}</div>
          <button className="icon-button" aria-label={dark?'切换为浅色模式':'切换为深色模式'} onClick={()=>setDark(!dark)}>{dark?<Sun size={19}/>:<Moon size={19}/>}</button>
          <button className="icon-button notification-trigger" aria-label="查看通知" onClick={()=>setNotices(true)}><Bell size={19}/><span/></button>
          <div className="dropdown-anchor"><button className="profile-trigger" aria-label="账户菜单" aria-expanded={dropdown==='user'} onClick={()=>setDropdown(dropdown==='user'?null:'user')}><span className="avatar">{(user?.name||user?.email||'NC').slice(0,2).toUpperCase()}</span><span className="profile-name">{user?.name||'我的账户'}</span><ChevronDown size={14}/></button>{dropdown==='user'&&<div className="dropdown profile-dropdown"><strong>{user?.name}</strong><small className="muted">{user?.email}</small><div className="dropdown-balance">账户余额 <b>{money(user?.balance)}</b></div><Link to="/account"><Users size={16}/>个人中心</Link>{user?.role==='admin'&&<Link to={admin?'/dashboard':'/admin/overview'}><Shield size={16}/>{admin?'服务中心':'管理后台'}</Link>}<button onClick={()=>void exit()}><LogOut size={16}/>退出登录</button></div>}</div>
        </div>
      </header>
      {dropdown&&<button className="dropdown-shade" aria-label="关闭菜单" onClick={()=>setDropdown(null)}/>}
      <main className="main-content" id="main-content">
        {settings.data?.demo&&<div className="demo-banner"><span><span className="tiny-dot"/>演示环境 · 数据仅用于体验</span><span>支付与真实节点尚未接入</span></div>}
        {current?(admin?<AdminPages key={current.path} page={current.path.split('/').pop()!}/>:<UserPages key={current.path} page={current.path.slice(1)}/>):<Card><Empty title="这个页面不存在" description="可以通过导航继续访问服务中心。" action={<Link className="button button-primary" to={admin?'/admin/overview':'/dashboard'}>返回首页</Link>}/></Card>}
        <footer className="main-footer"><span>© {new Date().getFullYear()} {settings.data?.site_name||'Nice Cloud'}</span><span>连接每一种可能</span></footer>
      </main>
      {!admin&&<nav className="mobile-bottom-nav" aria-label="移动导航">{[{path:'/dashboard',label:'首页',icon:LayoutDashboard},{path:'/plans',label:'商店',icon:ShoppingBag},{path:'/orders',label:'订单',icon:FileText},{path:'/docs',label:'文档',icon:BookOpen},{path:'/tickets',label:'工单',icon:Headphones},{path:'/account',label:'我的',icon:Users}].map(({path,label,icon:Icon})=><NavLink key={path} to={path} className={({isActive})=>isActive?'active':''}><Icon size={19}/><span>{label}</span></NavLink>)}</nav>}
    </div>
    {notices&&<NoticeModal onClose={()=>setNotices(false)}/>}
  </div>;
}

function NoticeModal({onClose}:{onClose:()=>void}) {
  const {data,loading,error,reload} = useResource<Article[]>('/notices');
  return <Modal title="公告与通知" onClose={onClose}>{loading?<Spinner/>:error?<Empty title="加载失败" description={error} action={<button className="button" onClick={()=>void reload()}>重试</button>}/>:data?.length?<div className="page-stack">{data.map(item=><article key={item.id} className="notification-item"><span className="notice-symbol"><Bell size={18}/></span><div><h3>{item.title}</h3><p className="article-body small">{item.body}</p></div></article>)}</div>:<Empty title="暂无新通知" description="新的服务通知会显示在这里。"/>}</Modal>;
}

function AuthPage({register=false}:{register?:boolean}) {
  const {refresh} = useAuth();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const {data:settings} = useResource<SiteSettings>('/settings/public');
  const [email,setEmail] = useState('');
  const [password,setPassword] = useState('');
  const [name,setName] = useState('');
  const [invite,setInvite] = useState(params.get('invite')||'');
  const [visible,setVisible] = useState(false);
  const [pending,setPending] = useState(false);
  const [error,setError] = useState('');
  const [help,setHelp] = useState(false);
  const toast = useToast();
  useEffect(()=>{setError('');},[register]);
  useEffect(()=>{document.title=`${register?'注册账户':'登录账户'} · ${settings?.site_name||'Nice Cloud'}`;},[register,settings?.site_name]);
  async function submit(event:FormEvent) {
    event.preventDefault();setPending(true);setError('');
    try {
      const next = await api<{role:string}>(register?'/auth/register':'/auth/login',{method:'POST',body:JSON.stringify({email,password,...(register?{name,invite_code:invite}:{} )})});
      await refresh();toast(register?'账户创建成功，欢迎加入':'登录成功，欢迎回来');navigate(next.role==='admin'?'/admin/overview':'/dashboard',{replace:true});
    }catch(err){setError(err instanceof Error?err.message:'操作失败，请重试');}finally{setPending(false);}
  }
  return <div className="auth-page"><div className="auth-art"><Link to="/login"><Logo/></Link><div className="auth-art-center"><div className="auth-cloud"><Cloud size={82} strokeWidth={1.2}/></div><span className="eyebrow">WELCOME TO NICE CLOUD</span><h1>连接世界，<br/>从这里开始。</h1><p>一处管理你的订阅、节点与服务，<br/>让每一次连接更加简单。</p><div className="auth-features"><span><Globe size={17}/>全球节点</span><span><Shield size={17}/>安全连接</span><span><Headphones size={17}/>贴心支持</span></div></div><div className="auth-art-footer">Nice Cloud · 连接每一种可能</div></div>
    <div className="auth-content"><Link className="auth-mobile-logo" to="/login"><Logo/></Link><div className="auth-form-wrap"><div className="auth-welcome-icon">{register?<Plus size={27}/>:<ArrowRight size={27}/>}</div><h1>{register?'创建你的账户':'欢迎回来'}</h1><p className="muted">{register?'加入 Nice Cloud，开启你的连接之旅。':'登录 Nice Cloud，继续使用你的服务。'}</p>
      <form className="auth-form" onSubmit={event=>void submit(event)}>
        {register&&<label className="form-field">昵称<input autoComplete="nickname" value={name} onChange={e=>setName(e.target.value)} placeholder="如何称呼你" maxLength={40} required/></label>}
        <label className="form-field">电子邮箱<input type="email" autoComplete="email" value={email} onChange={e=>setEmail(e.target.value)} placeholder="you@example.com" maxLength={254} required/></label>
        <label className="form-field">密码<div className="password-field"><input type={visible?'text':'password'} autoComplete={register?'new-password':'current-password'} value={password} onChange={e=>setPassword(e.target.value)} placeholder={register?'至少 10 位字符':'请输入密码'} minLength={register?10:1} maxLength={128} required/><button type="button" className="icon-button" aria-label={visible?'隐藏密码':'显示密码'} onClick={()=>setVisible(!visible)}>{visible?<EyeOff size={18}/>:<Eye size={18}/>}</button></div></label>
        {register&&<label className="form-field">邀请码 <span className="muted small">（选填）</span><input value={invite} onChange={e=>setInvite(e.target.value)} placeholder="输入朋友的邀请码" maxLength={40}/></label>}
        {!register&&<div className="auth-help"><span className="muted small"><Shield size={13}/> 安全账户登录</span><button type="button" onClick={()=>setHelp(true)}>无法登录？</button></div>}
        {error&&<div className="form-error" role="alert">{error}</div>}
        <button className="button button-primary auth-submit" disabled={pending}>{pending?'正在处理…':register?'创建账户':'登录账户'}<ArrowRight size={17}/></button>
      </form>
      <p className="auth-switch">{register?'已有账户？':'还没有账户？'} <Link to={register?'/login':'/register'}>{register?'立即登录':'立即注册'}</Link></p>
      {settings?.demo&&<div className="demo-login"><span className="muted small">演示环境 · 选择账户后点击登录</span><div className="actions"><button type="button" className="button button-ghost" onClick={()=>{setEmail('demo@example.test');setPassword('DemoCloud2026!');}}>体验用户端</button><button type="button" className="button button-ghost" onClick={()=>{setEmail('admin@example.test');setPassword('DemoCloud2026!');}}>体验管理端</button></div></div>}
    </div><div className="auth-copyright">© {new Date().getFullYear()} Nice Cloud</div></div>
    {help&&<Modal title="账户登录帮助" onClose={()=>setHelp(false)}><div className="page-stack"><p>请确认邮箱和密码输入正确。若仍无法登录，请联系站点支持处理账户问题。</p>{settings?.contact_email?<a className="button button-primary" href={`mailto:${settings.contact_email}`}>联系支持：{settings.contact_email}</a>:<p className="muted">站点暂未配置支持邮箱，请联系管理员。</p>}<p className="muted small">自助邮件找回密码暂未开通。</p><Link className="button button-ghost" to="/login" onClick={()=>setHelp(false)}><ArrowLeft size={15}/>返回登录</Link></div></Modal>}
  </div>;
}
