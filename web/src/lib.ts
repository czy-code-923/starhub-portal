export function traffic(bytes: unknown, unit='GB') {
 const n=Math.max(0,Number(bytes)||0), binary=unit==='GiB', k=binary?1024:1000;
 if(unit==='GB'||unit==='GiB')return `${(n/k**3).toLocaleString('zh-CN',{maximumFractionDigits:2})} ${unit}`;
 const i=Math.min(4,Math.max(0,Math.floor(Math.log(n||1)/Math.log(k))));
 return `${(n/k**i).toLocaleString('zh-CN',{maximumFractionDigits:2})} ${['B','KB','MB','GB','TB'][i]}`;
}
export const money=(n:unknown)=>'¥'+((Number(n)||0)/100).toFixed(2);
export const date=(v:unknown)=>v?new Date(String(v)).toLocaleString('zh-CN',{hour12:false}):'—';
export const states:Record<string,string>={pending:'待处理',paid:'已支付',cancelled:'已取消',late_paid:'超时付款 · 待人工处理',refund_pending:'待退款',refunded:'已退款',active:'已生效',none:'未开通',queued:'排队中',working:'处理中',failed:'失败',succeeded:'已完成',rolled_back:'已回滚',unverified:'待验证邮箱',disabled:'已停用',expired:'已过期',open:'进行中',closed:'已关闭'};
export function buyKey(){return globalThis.crypto?.randomUUID?.()||Array.from(globalThis.crypto.getRandomValues(new Uint8Array(20)),v=>v.toString(16).padStart(2,'0')).join('');}
