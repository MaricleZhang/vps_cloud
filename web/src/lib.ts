import {useCallback, useEffect, useRef, useState} from 'react';

export class ApiError extends Error { constructor(message:string, public status:number) {super(message);} }
export async function api<T>(path:string, options:RequestInit = {}):Promise<T> {
  const response = await fetch(path.startsWith('/api/') ? path : `/api${path}`, {
    ...options, credentials:'include', headers:{...(options.body ? {'Content-Type':'application/json'} : {}),...options.headers},
  });
  const payload = await response.json().catch(() => null) as {data:T;error?:string}|null;
  if (!response.ok) {
    if (response.status === 401 && !path.startsWith('/auth/')) window.dispatchEvent(new Event('cloud:session-expired'));
    throw new ApiError(payload?.error || `请求失败 (${response.status})`, response.status);
  }
  if (!payload || !('data' in payload)) throw new ApiError('服务返回了无法识别的数据', response.status);
  return payload.data;
}
export function useResource<T>(path:string) {
  const [data,setData] = useState<T>();
  const [loading,setLoading] = useState(true);
  const [error,setError] = useState('');
  const request = useRef(0);
  const reload = useCallback(async () => {
    const version = ++request.current;
    setLoading(true); setError('');
    try {const next = await api<T>(path); if (version === request.current) setData(next);}
    catch(err) {if (version === request.current) setError(err instanceof Error ? err.message : '加载失败');}
    finally {if (version === request.current) setLoading(false);}
  },[path]);
  useEffect(() => {setData(undefined);void reload();return () => {request.current++;};},[reload]);
  return {data,loading,error,reload};
}
export const money = (cents:number = 0) => `¥${(cents / 100).toLocaleString('zh-CN',{minimumFractionDigits:2,maximumFractionDigits:2})}`;
export function date(value?:string, withTime = false) {
  if (!value || value.startsWith('0001')) return '长期有效';
  const result = new Date(value);
  if (Number.isNaN(result.getTime())) return value;
  return result.toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',year:'numeric',month:'2-digit',day:'2-digit',...(withTime ? {hour:'2-digit',minute:'2-digit'} : {})});
}
export function gb(value:number = 0) {
  const safe = Math.max(0, value);
  const bytes = safe * 1073741824;
  if (bytes > 0 && bytes < 1024) return `${Math.round(bytes)} B`;
  if (bytes >= 1024 && bytes < 1048576) return `${(bytes / 1024).toFixed(1)} KB`;
  return safe < 1 && safe > 0 ? `${(safe * 1024).toFixed(1)} MB` : `${safe.toFixed(2)} GB`;
}

const bytesPerGB = 1073741824;
export function trafficGB(record: {upload_gb:number;download_gb:number;total_gb:number;upload_bytes?:number;download_bytes?:number;total_bytes?:number}, field: 'upload'|'download'|'total') {
  const bytes = record[`${field}_bytes`];
  return bytes === undefined ? record[`${field}_gb`] : bytes / bytesPerGB;
}
export function subscriptionGB(subscription: {traffic_limit:number;traffic_used:number;traffic_limit_bytes?:number;traffic_used_bytes?:number}, field: 'limit'|'used') {
  const bytes = subscription[`traffic_${field}_bytes`];
  return bytes === undefined ? subscription[`traffic_${field}`] : bytes / bytesPerGB;
}
export function serviceReason(reason?:string) {
  const labels:Record<string,string> = {active:'可用',quota_exceeded:'流量已用尽',expired:'订阅已到期',suspended:'订阅已暂停',user_disabled:'账户已停用',account_disabled:'账户已停用',disabled:'账户已停用',node_disabled:'节点已停用',no_subscription:'暂无订阅',no_port:'端口额度不足',port_exhausted:'端口额度不足',agent_error:'节点执行失败',unconfigured:'节点待配置'};
  return reason ? labels[reason] || reason : '待确认';
}
