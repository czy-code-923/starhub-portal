export type RequestFn=(path:string,method?:string,body?:unknown)=>Promise<any>;
export async function imageData(file:File):Promise<string>{
 if(!['image/png','image/jpeg'].includes(file.type)||file.size>8*1024*1024)throw new Error('请选择 8MB 以内 PNG/JPEG 图片');
 const bitmap=await createImageBitmap(file);try{if(bitmap.width>12000||bitmap.height>12000)throw new Error('图片像素过大');const scale=Math.min(1,1600/Math.max(bitmap.width,bitmap.height));const canvas=document.createElement('canvas');canvas.width=Math.max(1,Math.round(bitmap.width*scale));canvas.height=Math.max(1,Math.round(bitmap.height*scale));const ctx=canvas.getContext('2d');if(!ctx)throw new Error('当前浏览器不支持图片处理');ctx.fillStyle='#fff';ctx.fillRect(0,0,canvas.width,canvas.height);ctx.drawImage(bitmap,0,0,canvas.width,canvas.height);const data=canvas.toDataURL('image/jpeg',.88);if(data.length>1398200)throw new Error('图片压缩后仍大于 1MB，请裁剪后重试');return data;}finally{bitmap.close()}
}
export function cents(value:string):number{if(!/^\d{1,7}(\.\d{1,2})?$/.test(value))throw new Error('金额格式应为人民币元，最多两位小数');const [whole,fraction='']=value.split('.');return Number(whole)*100+Number(fraction.padEnd(2,'0'))}
