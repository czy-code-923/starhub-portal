// @vitest-environment happy-dom
import {mount,flushPromises} from '@vue/test-utils';import {describe,it,expect,vi} from 'vitest';
import ClientHelp from './ClientHelp.vue';import TelegramPanel from './TelegramPanel.vue';import AccountDeletion from './AccountDeletion.vue';import PlanCatalog from './PlanCatalog.vue';
describe('editable customer help',()=>{
 it('loads persisted help, edits steps and sends an optimistic version',async()=>{
  const document={title:'平台教程',intro:'说明',apps:[{id:'one',name:'Example',format:'Mihomo',platforms:['Android'],url:'https://example.test',steps:['原始步骤']}],faqs:[]};
  const request=vi.fn(async(_p:string,m?:string,b?:any)=>{if(m==='PUT'){Object.assign(document,b.document);return{message:'ok'}}return{document:JSON.parse(JSON.stringify(document)),version:3}});
  const w=mount(ClientHelp,{props:{request,editable:true}});await flushPromises();expect(w.text()).toContain('原始步骤');await w.findAll('button').find(b=>b.text()==='编辑教程')!.trigger('click');
  await w.find('input[maxlength="100"]').setValue('管理员教程');await w.find('textarea[rows="4"]').setValue('复制链接\n选择对应格式\n启动配置');await w.find('form').trigger('submit');await flushPromises();
  expect(request).toHaveBeenCalledWith('admin/client-help','PUT',expect.objectContaining({version:3,document:expect.objectContaining({title:'管理员教程',apps:[expect.objectContaining({steps:['复制链接','选择对应格式','启动配置']})]})}));expect(w.text()).toContain('启动配置');w.unmount();
 });
 it('only shows editing to authorized administrators',async()=>{const w=mount(ClientHelp,{props:{request:vi.fn().mockResolvedValue({document:{title:'Guide',intro:'',apps:[],faqs:[]},version:0})}});await flushPromises();expect(w.text()).not.toContain('编辑教程');w.unmount()});
});
describe('Telegram approval setup regression',()=>{
 it('unwraps the actual administrator response and allows binding a reviewer',async()=>{
  const request=vi.fn(async(path:string,m?:string)=>m==='PUT'?{message:'saved'}:path==='admin/administrators'?{items:[{id:'a'.repeat(32),name:'Owner',email:'owner@example.test',is_owner:true,admin_scopes:[],status:'active'}],scopes:{payments:'Payments'}}:{config:{enabled:true,mode:'polling',principals:[{user_id:42,scopes:['payments'],notify:true}]},version:2,configured:true,scopes:{payments:'付款权限',approve:'核验红包 / 收款码并开通'},status:{}});
  const w=mount(TelegramPanel,{props:{request}});await flushPromises();await w.findAll('label').find(l=>l.text().includes('核验红包 / 收款码并开通'))!.find('input').setValue(true);await flushPromises();
  expect(w.find('select[required]').text()).toContain('Owner');await w.find('select[required]').setValue('a'.repeat(32));await w.find('form').trigger('submit');await flushPromises();
  expect(request).toHaveBeenCalledWith('admin/telegram','PUT',expect.objectContaining({principals:[expect.objectContaining({administrator_id:'a'.repeat(32),scopes:['payments','approve']})]}));w.unmount();
 });
});
describe('account deletion',()=>{
 it('requires explicit confirmation and includes the current record version',async()=>{
  const request=vi.fn().mockResolvedValue({message:'账户已删除'}),w=mount(AccountDeletion,{props:{request,kind:'customers',target:{id:'a'.repeat(32),name:'Example',email:'e@example.test',admin_version:4}}});
  await w.find('form').trigger('submit');expect(request).not.toHaveBeenCalled();await w.find('input[type=checkbox]').setValue(true);await w.find('form').trigger('submit');await flushPromises();expect(request).toHaveBeenCalledWith('admin/customers','DELETE',{id:'a'.repeat(32),version:4,confirmed:true});expect(w.emitted('changed')).toHaveLength(1);w.unmount();
 });
});
describe('consistent plan catalog',()=>{
 it('uses the same card structure across categories and preserves checkout identity',async()=>{
  const plan=(id:string,c:string,recommended:boolean)=>({id,category_id:c,name:id,description:'说明',prices:[{amount:1000,months:1}],quota_bytes:20*1024**3,quota_unit:'GiB',protocols:['hysteria2'],max_devices:2,display_features:[],recommended});
  const w=mount(PlanCatalog,{props:{admin:false,catalog:{categories:[{id:'light',name:'轻享'},{id:'daily',name:'日常畅连'}],plans:[plan('one','light',false),plan('two','daily',true)]}}});const cards=w.findAll('.catalog-plan');expect(cards).toHaveLength(2);expect(cards.every(c=>c.find('.catalog-facts').exists()&&c.find('.plan-labels').exists())).toBe(true);expect(w.find('.featured').exists()).toBe(false);await cards[1]!.find('button').trigger('click');expect(w.emitted('choose')?.[0]?.[0]).toMatchObject({id:'two'});w.unmount();
 });
});
