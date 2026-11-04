// @vitest-environment happy-dom
import {mount,flushPromises} from '@vue/test-utils';
import {describe,it,expect,vi} from 'vitest';
import AccountPanel from './AccountPanel.vue';
import OrderPayment from './OrderPayment.vue';
import TelegramPanel from './TelegramPanel.vue';

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

describe('red packet and Telegram settings',()=>{
 it('binds a passcode without showing a QR code or opening service',async()=>{
  let submitted=false;
  const request=vi.fn(async(_path:string,method?:string)=>method==='POST'?(submitted=true,{message:'等待领取核实'}):{attempt:{method_id:'alipay_redpacket',method_name:'支付宝口令红包',state:submitted?'submitted':'awaiting',order_state:'pending',version:1,redpacket_submitted:submitted,expires_at:new Date(Date.now()+3600000).toISOString()}});
  const w=mount(OrderPayment,{props:{request,order:{id:'redpacket-order',payable:990}}});await flushPromises();
  expect(w.find('img.payment-qr').exists()).toBe(false);expect(w.find('button.primary').attributes('disabled')).toBeDefined();
  await w.find('input[type=password]').setValue('TEST-ONLY-PASSCODE');await w.find('form').trigger('submit');await flushPromises();
  expect(request).toHaveBeenCalledWith('orders/redpacket-order/receipt','POST',expect.objectContaining({redpacket_password:'TEST-ONLY-PASSCODE'}));
  expect(w.find('input[type=password]').element).toHaveProperty('value','');expect(w.text()).toContain('等待领取核实');w.unmount();
 });
 it('keeps saved bot tokens private and saves explicit recipient scopes',async()=>{
  const request=vi.fn(async(_path:string,method?:string)=>method==='PUT'?{message:'已保存'}:{config:{enabled:false,mode:'notify_only',principals:[]},version:1,configured:true,scopes:{orders:'订单查询',payments:'核款查询',redpacket:'查看口令'},status:{}});
  const w=mount(TelegramPanel,{props:{request}});await flushPromises();
  expect(w.find('input[type=password]').element).toHaveProperty('value','');
  await w.findAll('button').find(b=>b.text().includes('添加授权对象'))!.trigger('click');
  await w.find('input[type=number]').setValue(42);await w.find('form').trigger('submit');await flushPromises();
  expect(request).toHaveBeenCalledWith('admin/telegram','PUT',expect.objectContaining({token:'',principals:[expect.objectContaining({user_id:42,scopes:['orders','payments']})]}));w.unmount();
 });
});
