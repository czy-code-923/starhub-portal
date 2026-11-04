import {describe,it,expect} from 'vitest';
import {traffic,money,buyKey} from './lib';
describe('billing display and checkout keys',()=>{it('uses decimal GB without relabeling binary values',()=>{expect(traffic(100000000000,'GB')).toBe('100 GB');expect(traffic(1073741824,'GiB')).toBe('1 GiB');expect(traffic(0,'auto')).toBe('0 B')});it('formats cents exactly',()=>{expect(money(101)).toBe('¥1.01')});it('never reuses an order request key',()=>{expect(buyKey()).not.toBe(buyKey())})});
