type Row=Record<string,any>;
export function availableProtocols(groups:Row[],ids:string[]=[]):string[]{return [...new Set(groups.filter(g=>ids.includes(g.id)).flatMap(g=>(g.services||[]).filter((s:Row)=>s.managed_users).map((s:Row)=>String(s.protocol))))].sort()}
export function configuredProtocols(plan:Row,groups:Row[]):string[]{const available=new Set(availableProtocols(groups,plan.group_ids||[]));return(plan.protocols||[]).filter((p:string)=>available.has(p))}
