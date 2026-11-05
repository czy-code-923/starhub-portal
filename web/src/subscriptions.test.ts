// @vitest-environment happy-dom
import {mount,flushPromises} from '@vue/test-utils';import {afterEach,describe,expect,it,vi} from 'vitest';
import SubscriptionsPanel from './SubscriptionsPanel.vue';import App from './App.vue';
afterEach(()=>{vi.unstubAllGlobals();vi.restoreAllMocks()});
describe('per-order subscriptions',()=>{
 it('opens the requested older order outside the first page and downloads its actual config',async()=>{
  const id='b'.repeat(32),first='a'.repeat(32);const request=vi.fn(async(path:string)=>{
   if(path.startsWith('me/services'))return{items:[{id:first,plan_name:'First page',quota_unit:'GiB'}],total:26};
   if(path==='clients')return[{id:'mihomo',name:'Mihomo'}];if(path.startsWith('me/service?'))return{service:{plan_name:'Older order',order_id:id,quota_unit:'GiB',quota_bytes:20*1024**3}};
   if(path.startsWith('me/devices'))return[];if(path.includes('/check'))return{ready:true};return{url:'https://example.test/own-subscription'};
  });
  const download=vi.fn().mockResolvedValue(new Response('proxies: []\n',{headers:{'Content-Disposition':'attachment; filename="starhub-'+id+'.yaml"'}}));vi.stubGlobal('fetch',download);
  vi.spyOn(URL,'createObjectURL').mockReturnValue('blob:fixture');vi.spyOn(URL,'revokeObjectURL').mockImplementation(()=>{});const click=vi.spyOn(HTMLAnchorElement.prototype,'click').mockImplementation(()=>{});
  const w=mount(SubscriptionsPanel,{props:{request,initial:id}});await flushPromises();expect(w.text()).toContain('Older order');expect(request).toHaveBeenCalledWith('me/subscription/share?service='+id+'&client=auto');
  await w.findAll('button').find(b=>b.text().includes('下载'))!.trigger('click');await flushPromises();expect(download).toHaveBeenCalledWith('/portal/v1/me/subscription/content?service='+id+'&client=mihomo',expect.objectContaining({credentials:'same-origin'}));expect(click).toHaveBeenCalledOnce();w.unmount();
 });
 it('shows HTTP download failures instead of saving an error document',async()=>{
  const id='a'.repeat(32),request=vi.fn(async(path:string)=>path.startsWith('me/services')?{items:[{id,plan_name:'Example'}],total:1}:path==='clients'?[]:path.startsWith('me/service?')?{service:{plan_name:'Example'}}:path.startsWith('me/devices')?[]:path.includes('/check')?{ready:true}:{url:'https://example.test/sub'});
  vi.stubGlobal('fetch',vi.fn().mockResolvedValue(new Response(JSON.stringify({error:'节点同步中'}),{status:503})));const click=vi.spyOn(HTMLAnchorElement.prototype,'click').mockImplementation(()=>{});
  const w=mount(SubscriptionsPanel,{props:{request}});await flushPromises();await w.findAll('button').find(b=>b.text().includes('下载'))!.trigger('click');await flushPromises();expect(w.text()).toContain('节点同步中');expect(click).not.toHaveBeenCalled();w.unmount();
 });
});
describe('customer presentation',()=>{
 it('shows multiple pinned announcements and safely wraps custom plan facts without billing policy',async()=>{
  const user={role:'customer',name:'Example',email:'e@example.test'};
  vi.stubGlobal('fetch',vi.fn(async(input:string)=>{const path=input.replace('/portal/v1/','');const body=path==='public'?{title:'Test portal',announcements:[{id:'1',title:'Pinned one',pinned:true},{id:'2',title:'Pinned two',pinned:true}]}:path==='me'?{user,csrf:'fixture'}:path==='me/service'?{service:null}:path==='catalog'?{categories:[{id:'c',name:'Basic'}],plans:[{id:'p',category_id:'c',name:'20 GiB',quota_bytes:20*1024**3,quota_unit:'GiB',protocols:['vless'],prices:[{amount:1000,months:1}],display_features:[{label:'峰值速率',value:'500 Mbps'},{label:'备注',value:'<img src=x onerror=alert(1)>'}]}]}:[];return new Response(JSON.stringify(body))}));
  const w=mount(App);await flushPromises();expect(w.text().match(/置顶/g)).toHaveLength(2);await w.findAll('nav button').find(b=>b.text().includes('选购套餐'))!.trigger('click');await flushPromises();expect(w.text()).toContain('500 Mbps');expect(w.text()).toContain('20 GiB');expect(w.text()).not.toContain('计费倍率');expect(w.find('.catalog-facts img').exists()).toBe(false);w.unmount();
 });
});
