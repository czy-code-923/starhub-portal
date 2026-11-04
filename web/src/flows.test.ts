// @vitest-environment happy-dom
import {mount,flushPromises} from '@vue/test-utils';
import {describe,it,expect,vi} from 'vitest';
import AccountPanel from './AccountPanel.vue';
import OrderPayment from './OrderPayment.vue';

describe('account and manual payment flows',()=>{
 it('blocks mismatched passwords and only sends confirmed changes',async()=>{
  const request=vi.fn().mockResolvedValue({message:'请重新登录'});
  const w=mount(AccountPanel,{props:{request,user:{name:'Example',email:'example@example.test'}}});
  const fields=w.findAll('input[type=password]');
  await fields[0]!.setValue('current-password');await fields[1]!.setValue('replacement-password');await fields[2]!.setValue('mismatch-password');
  expect(w.text()).toContain('两次输入的密码不一致');expect(w.findAll('button')[1]!.attributes('disabled')).toBeDefined();
  await w.findAll('form')[1]!.trigger('submit');expect(request).not.toHaveBeenCalled();
  await fields[2]!.setValue('replacement-password');await w.findAll('form')[1]!.trigger('submit');await flushPromises();
  expect(request).toHaveBeenCalledWith('me/password','PUT',expect.objectContaining({password_confirm:'replacement-password'}));
  expect(w.emitted('signedOut')).toHaveLength(1);w.unmount();
 });
 it('receipt submission remains pending instead of treating a screenshot as paid',async()=>{
  const now=Date.now();let submitted=false;
  const attempt=()=>({state:submitted?'submitted':'awaiting',version:submitted?2:1,order_state:'pending',expires_at:new Date(now+3600000).toISOString(),review_deadline:new Date(now+172800000).toISOString(),method_name:'微信个人收款码',proof_available:false});
  const request=vi.fn(async(path:string,method?:string)=>{if(method==='POST'){submitted=true;return{message:'等待实际到账核实'}}return{attempt:attempt()}});
  const w=mount(OrderPayment,{props:{request,order:{id:'order-one',payable:990,expires_at:new Date(now+3600000).toISOString()}}});await flushPromises();
  expect(w.find('img').attributes('src')).toBe('/portal/v1/orders/order-one/payment-code');
  await w.find('input[maxlength="100"]').setValue('REAL-TRANSACTION-123');await w.find('form').trigger('submit');await flushPromises();
  expect(request).toHaveBeenCalledWith('orders/order-one/receipt','POST',expect.objectContaining({reference:'REAL-TRANSACTION-123',version:1}));
  expect(w.text()).toContain('等待实际到账核实');expect(w.emitted('changed')).toHaveLength(1);expect(w.emitted('close')).toBeUndefined();w.unmount();
 });
});
