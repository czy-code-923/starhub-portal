// @vitest-environment happy-dom
import {mount,flushPromises} from '@vue/test-utils';import {afterEach,describe,expect,it,vi} from 'vitest';
import SubscriptionsPanel from './SubscriptionsPanel.vue';import App from './App.vue';
afterEach(()=>{vi.unstubAllGlobals();vi.restoreAllMocks()});
describe('per-order subscriptions',()=>{
 it('opens an older order and exposes independent links for each format without file downloads',async()=>{
  const id='b'.repeat(32),first='a'.repeat(32);const request=vi.fn(async(path:string)=>{
   if(path.startsWith('me/services'))return{items:[{id:first,plan_name:'First page',quota_unit:'GiB'}],total:26};
   if(path==='clients')return[{id:'mihomo',name:'Mihomo'},{id:'sing-box',name:'sing-box'}];if(path.startsWith('me/service?'))return{service:{state:'active',plan_name:'Older order',order_id:id,quota_unit:'GiB',quota_bytes:20*1024**3}};
   if(path.startsWith('me/devices'))return[];if(path.includes('/check'))return{ready:true};return{url:'https://example.test/'+new URLSearchParams(path.split('?')[1]).get('client')};
  });
  const fetcher=vi.fn();vi.stubGlobal('fetch',fetcher);const w=mount(SubscriptionsPanel,{props:{request,initial:id}});await flushPromises();expect(w.text()).toContain('Older order');
  await w.find('[data-client="mihomo"] button').trigger('click');await flushPromises();expect(request).toHaveBeenCalledWith('me/subscription/share?service='+id+'&client=mihomo');expect(w.find('[aria-label="Mihomo 订阅链接"]').element).toHaveProperty('value','https://example.test/mihomo');
  await w.find('[data-client="sing-box"] button').trigger('click');await flushPromises();expect(request).toHaveBeenCalledWith('me/subscription/share?service='+id+'&client=sing-box');expect(w.find('[aria-label="sing-box 订阅链接"]').element).toHaveProperty('value','https://example.test/sing-box');
  expect(w.findAll('button').some(b=>b.text().includes('下载'))).toBe(false);expect(fetcher).not.toHaveBeenCalled();w.unmount();
 });
 it('shows deleted services without exposing or requesting import links',async()=>{
  const id='a'.repeat(32),request=vi.fn(async(path:string)=>path.startsWith('me/services')?{items:[{id,plan_name:'Example',service_state:'deleted'}],total:1}:path==='clients'?[{id:'mihomo',name:'Mihomo'}]:path.startsWith('me/service?')?{service:{state:'deleted',plan_name:'Example'}}:[]);
  const w=mount(SubscriptionsPanel,{props:{request}});await flushPromises();expect(w.text()).toContain('已删除');expect(w.find('.client-grid').exists()).toBe(false);expect(request.mock.calls.some(c=>c[0].includes('/share')||c[0].includes('/devices'))).toBe(false);w.unmount();
 });
 it('clears previous links when switching orders and excludes incompatible formats',async()=>{
  const a='a'.repeat(32),b='b'.repeat(32),request=vi.fn(async(path:string)=>path.startsWith('me/services')?{items:[{id:a,plan_name:'First'},{id:b,plan_name:'Second'}],total:2}:path==='clients'?[{id:'mihomo',name:'Mihomo'},{id:'other',name:'Unsupported'}]:path.startsWith('me/service?')?{service:{state:'active'}}:path.startsWith('me/devices')?[]:path.includes('/check')?{ready:!path.endsWith('other'),reason:'不支持此协议'}:{url:'https://example.test/'+(path.includes(a)?a:b)});
  const w=mount(SubscriptionsPanel,{props:{request}});await flushPromises();await w.find('[data-client="mihomo"] button').trigger('click');await flushPromises();expect(w.find('[aria-label="Mihomo 订阅链接"]').element).toHaveProperty('value','https://example.test/'+a);
  await w.findAll('.service-open')[1]!.trigger('click');await flushPromises();expect(w.find('[aria-label="Mihomo 订阅链接"]').exists()).toBe(false);await w.find('[data-client="mihomo"] button').trigger('click');await flushPromises();expect(w.find('[aria-label="Mihomo 订阅链接"]').element).toHaveProperty('value','https://example.test/'+b);
  await w.find('[data-client="other"] button').trigger('click');await flushPromises();expect(w.text()).toContain('不支持此协议');expect(request).not.toHaveBeenCalledWith('me/subscription/share?service='+b+'&client=other');w.unmount();
 });
});
describe('customer presentation',()=>{
 it('shows multiple pinned announcements and safely wraps custom plan facts without billing policy',async()=>{
  const user={role:'customer',name:'Example',email:'e@example.test'};
  vi.stubGlobal('fetch',vi.fn(async(input:string)=>{const path=input.replace('/portal/v1/','');const body=path==='public'?{title:'Test portal',announcements:[{id:'1',title:'Pinned one',pinned:true},{id:'2',title:'Pinned two',pinned:true}]}:path==='me'?{user,csrf:'fixture'}:path==='me/service'?{service:null}:path==='catalog'?{categories:[{id:'c',name:'Basic'}],plans:[{id:'p',category_id:'c',name:'20 GiB',quota_bytes:20*1024**3,quota_unit:'GiB',protocols:['vless'],prices:[{amount:1000,months:1}],display_features:[{label:'峰值速率',value:'500 Mbps'},{label:'备注',value:'<img src=x onerror=alert(1)>'}]}]}:[];return new Response(JSON.stringify(body))}));
  const w=mount(App);await flushPromises();expect(w.text().match(/置顶/g)).toHaveLength(2);await w.findAll('nav button').find(b=>b.text().includes('选购套餐'))!.trigger('click');await flushPromises();expect(w.text()).toContain('500 Mbps');expect(w.text()).toContain('20 GiB');expect(w.text()).not.toContain('计费倍率');expect(w.find('.catalog-facts img').exists()).toBe(false);w.unmount();
 });
});
