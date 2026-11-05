// @vitest-environment happy-dom
import {mount,flushPromises} from '@vue/test-utils';import {reactive} from 'vue';import {describe,it,expect,vi} from 'vitest';
import PlanProtocolPicker from './PlanProtocolPicker.vue';import SubscriptionsPanel from './SubscriptionsPanel.vue';import ConfirmRecordAction from './ConfirmRecordAction.vue';
describe('actual plan protocol selection',()=>{
 it('never preselects choices and removes selections absent from the selected groups',async()=>{
  const groups=[{id:'one',name:'VLESS group',services:[{name:'vless node',protocol:'vless',managed_users:true},{name:'Snell shared',protocol:'snell',managed_users:false}]},{id:'two',name:'QUIC group',services:[{name:'hy2 node',protocol:'hysteria2',managed_users:true}]}];
  const model=reactive({group_ids:['one'],protocols:[] as string[]});const w=mount(PlanProtocolPicker,{props:{model,groups,ready:true}});await flushPromises();
  expect(w.find('.protocol-options').text()).toBe('VLESS');expect(w.text()).toContain('当前已选：无');expect(w.find('.protocol-options input').element).toHaveProperty('checked',false);
  await w.find('.protocol-options input').setValue(true);expect(model.protocols).toEqual(['vless']);
  model.group_ids=['two'];await flushPromises();expect(model.protocols).toEqual([]);expect(w.find('.protocol-options').text()).toBe('HYSTERIA2');expect(w.find('.protocol-options input').element).toHaveProperty('checked',false);w.unmount();
 });
});
describe('recoverable record removal',()=>{
 it('requires confirmation and transmits optimistic plan version',async()=>{
  const request=vi.fn().mockResolvedValue({message:'已删除'}),w=mount(ConfirmRecordAction,{props:{request,path:'admin/plans',method:'DELETE',payload:{id:'p',version:7},title:'删除套餐',message:'保留订单'}});
  await w.find('form').trigger('submit');expect(request).not.toHaveBeenCalled();await w.find('input').setValue(true);await w.find('form').trigger('submit');await flushPromises();expect(request).toHaveBeenCalledWith('admin/plans','DELETE',{id:'p',version:7,confirmed:true});expect(w.emitted('changed')?.[0]).toEqual(['已删除']);w.unmount();
 });
 it('removes an inactive record, lists hidden records and restores without exposing links',async()=>{
  const id='a'.repeat(32);let hidden=false;const request=vi.fn(async(path:string,method?:string)=>{
   if(method==='DELETE'){hidden=true;return{message:'记录已移除'}}if(method==='POST'){hidden=false;return{message:'已恢复'}}
   if(path.startsWith('me/services?'))return{items:(path.includes('hidden=1')===hidden)?[{id,plan_name:'Old',service_state:'expired'}]:[],total:path.includes('hidden=1')===hidden?1:0};
   if(path==='clients')return[];if(path.startsWith('me/service?'))return{service:{state:'expired',renewal_window_open:false,renewal_unavailable_reason:'订阅已过期超过 3 天，不能续费'}};return[];
  });const w=mount(SubscriptionsPanel,{props:{request}});await flushPromises();expect(w.text()).not.toContain('续费此订阅');
  await w.find('.record-remove').trigger('click');await w.find('[role=dialog] input').setValue(true);await w.find('[role=dialog]').trigger('submit');await flushPromises();expect(request).toHaveBeenCalledWith('me/services/'+id,'DELETE',{confirmed:true});expect(w.findAll('.service-card')).toHaveLength(0);
  await w.find('input[type=checkbox]').setValue(true);await flushPromises();expect(w.find('.service-open').exists()).toBe(false);expect(w.text()).toContain('恢复显示');
  await w.find('.record-remove').trigger('click');await w.find('[role=dialog] input').setValue(true);await w.find('[role=dialog]').trigger('submit');await flushPromises();expect(request).toHaveBeenCalledWith('me/services/'+id+'/restore','POST',{confirmed:true});expect(request.mock.calls.some(c=>c[0].includes('/share'))).toBe(false);w.unmount();
 });
});
