import {it,expect} from 'vitest';import {cents} from './media';
it('converts reviewed payment amounts using integer cents',()=>{expect(cents('10.01')).toBe(1001);expect(cents('0.10')).toBe(10);for(const value of ['1e2','-2','1.001','NaN','Infinity'])expect(()=>cents(value)).toThrow()});
