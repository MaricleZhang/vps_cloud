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
export function gb(value:number = 0) { return value < 1 && value > 0 ? `${(value*1024).toFixed(1)} MB` : `${Math.max(0,value).toFixed(2)} GB`; }
