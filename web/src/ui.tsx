import {createContext,useCallback,useContext,useEffect,useId,useRef,useState} from 'react';
import type {ReactNode} from 'react';
import {createPortal} from 'react-dom';
import {CheckCircle2,Cloud,Inbox,LoaderCircle,X,XCircle} from 'lucide-react';
import {api,ApiError} from './lib';
import type {User} from './types';

type AuthContextType = {user:User|null;loading:boolean;error:string;refresh:()=>Promise<void>;logout:()=>Promise<void>};
const AuthContext = createContext<AuthContextType|null>(null);
export function AuthProvider({children}:{children:ReactNode}) {
  const [user,setUser] = useState<User|null>(null);
  const [loading,setLoading] = useState(true);
  const [error,setError] = useState('');
  const refresh = useCallback(async () => {
    try {setUser(await api<User>('/auth/me'));setError('');}
    catch(err) {setUser(null);setError(err instanceof ApiError && err.status === 401 ? '' : err instanceof Error ? err.message : '连接服务失败');}
    finally {setLoading(false);}
  },[]);
  const logout = useCallback(async () => {await api('/auth/logout',{method:'POST'});setUser(null);},[]);
  useEffect(() => {void refresh(); const expire = () => void refresh();window.addEventListener('cloud:session-expired',expire);return () => window.removeEventListener('cloud:session-expired',expire);},[refresh]);
  return <AuthContext.Provider value={{user,loading,error,refresh,logout}}>{children}</AuthContext.Provider>;
}
export function useAuth() {const result = useContext(AuthContext);if (!result) throw new Error('AuthProvider missing');return result;}
type ToastFn = (message:string,type?:'success'|'error')=>void;
const ToastContext = createContext<ToastFn>(()=>{});
export function ToastProvider({children}:{children:ReactNode}) {
  const [items,setItems] = useState<{id:number;message:string;type:string}[]>([]);
  const id = useRef(0);
  const timers = useRef<ReturnType<typeof setTimeout>[]>([]);
  const toast = useCallback<ToastFn>((message,type='success') => {const key = ++id.current;setItems(old=>[...old.slice(-3),{id:key,message,type}]);timers.current.push(setTimeout(()=>setItems(old=>old.filter(item=>item.id!==key)),4500));},[]);
  useEffect(()=>()=>{timers.current.forEach(clearTimeout);},[]);
  return <ToastContext.Provider value={toast}>{children}<div className="toast-container" aria-live="polite">{items.map(item=><div role={item.type==='error'?'alert':'status'} className={`toast ${item.type}`} key={item.id}>{item.type==='error'?<XCircle size={19}/>:<CheckCircle2 size={19}/>}<span>{item.message}</span><button aria-label="关闭提示" onClick={()=>setItems(old=>old.filter(i=>i.id!==item.id))}><X size={15}/></button></div>)}</div></ToastContext.Provider>;
}
export const useToast = () => useContext(ToastContext);
export function Logo({compact=false,name='Nice Cloud'}:{compact?:boolean;name?:string}) {return <div className="brand"><span className="logo-mark"><Cloud size={29} strokeWidth={3} fill="currentColor"/></span>{!compact && <span className="brand-text">{name}<small>{name.toUpperCase()}</small></span>}</div>;}
export function Hero({title,description,children}:{title:string;description?:string;children?:ReactNode}) {return <header className="hero"><div><h1>{title}</h1>{description&&<p>{description}</p>}{children&&<div className="hero-actions">{children}</div>}</div><span className="hero-orbit" aria-hidden="true"/></header>;}
export function Card({children,className=''}:{children:ReactNode;className?:string}) {return <section className={`card ${className}`}>{children}</section>;}
export function Stat({label,value,icon,tone='purple',detail}:{label:string;value:ReactNode;icon?:ReactNode;tone?:string;detail?:string}) {return <div className="stat"><div className={`stat-icon tone-${tone}`}>{icon||<Cloud size={24}/>}</div><div className="stat-copy"><div className="stat-value">{value}</div><div className="stat-label">{label}</div>{detail&&<small className="muted">{detail}</small>}</div></div>;}
export function Empty({title,description,action}:{title:string;description?:string;action?:ReactNode}) {return <div className="empty"><div className="empty-icon"><Inbox size={32} strokeWidth={1.4}/></div><h3>{title}</h3>{description&&<p>{description}</p>}{action}</div>;}
export function Spinner() {return <div className="loading" role="status"><LoaderCircle className="spin" size={25}/><span>正在加载…</span></div>;}
const labels:Record<string,string> = {active:'正常',enabled:'已启用',disabled:'已停用',suspended:'已停用',banned:'已封禁',expired:'已到期',pending:'待处理',pending_payment:'待支付',unpaid:'待支付',paid:'已支付',completed:'已完成',cancelled:'已取消',canceled:'已取消',open:'处理中',closed:'已关闭',answered:'已回复',online:'在线',offline:'离线',unconfigured:'未接入',unknown:'待检测',approved:'已批准',rejected:'已拒绝',settled:'已结算',confirmed:'已确认',admin:'管理员',user:'用户',recurring:'周期套餐',onetime:'一次性',low:'低',normal:'普通',high:'紧急'};
Object.assign(labels,{not_connected:'未接入',demo:'演示节点',available:'可提现'});
export function Status({value}:{value:string}) {const color = ['active','enabled','paid','completed','online','approved','settled','confirmed','available'].includes(value)?'green':['banned','expired','rejected','offline'].includes(value)?'red':['pending','pending_payment','unpaid','open','high'].includes(value)?'orange':value==='demo'?'demo':'gray';return <span className={`status status-${color}`}><i/>{labels[value]||value||'—'}</span>;}
export function Modal({title,children,onClose,wide=false}:{title:string;children:ReactNode;onClose:()=>void;wide?:boolean}) {
  const panel = useRef<HTMLDivElement>(null);
  const close = useRef(onClose);close.current=onClose;
  const label = useId();
  useEffect(()=>{
    const previous = document.activeElement as HTMLElement|null;
    const oldOverflow = document.body.style.overflow;document.body.style.overflow='hidden';
    const focusable = () => panel.current?.querySelectorAll<HTMLElement>('button:not(:disabled),a[href],input:not(:disabled),textarea:not(:disabled),select:not(:disabled),[tabindex="0"]');
    focusable()?.[0]?.focus();
    const handler = (event:KeyboardEvent) => {if(event.key==='Escape') close.current();if(event.key==='Tab'){const list=focusable();if(!list?.length)return;const first=list[0],last=list[list.length-1];if(event.shiftKey&&document.activeElement===first){event.preventDefault();last.focus();}else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first.focus();}}};
    document.addEventListener('keydown',handler);
    return ()=>{document.removeEventListener('keydown',handler);document.body.style.overflow=oldOverflow;previous?.focus();};
  },[]);
  return createPortal(<div className="modal-backdrop" onMouseDown={e=>{if(e.target===e.currentTarget)onClose();}}><div className={`modal ${wide?'modal-wide':''}`} role="dialog" aria-modal="true" aria-labelledby={label} ref={panel}><div className="modal-header"><h2 id={label}>{title}</h2><button className="icon-button" aria-label="关闭窗口" onClick={onClose}><X size={20}/></button></div><div className="modal-body">{children}</div></div></div>,document.body);
}
