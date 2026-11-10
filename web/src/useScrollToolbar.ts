import {onBeforeUnmount,onMounted,ref,watch,type Ref} from 'vue';
// Passive, frame-bounded observation; no polling and no wheel/touch interception.
export function useScrollToolbar(enabled:Ref<boolean>,page:Ref<string>){
 const hidden=ref(false);let last=0,anchor=0,direction=0,frame=0,keyboard=false,motion:MediaQueryList|undefined;
 function show(){hidden.value=false;last=window.scrollY;anchor=last;direction=0}
 function measure(){frame=0;const y=Math.max(0,window.scrollY),previous=last,delta=y-last;last=y;const active=document.activeElement as HTMLElement|null;if(!enabled.value||motion?.matches||y<80||window.innerHeight+y>=document.documentElement.scrollHeight-24||active?.matches('input,textarea,select,[contenteditable=true]')&&!active?.closest('.workspace-toolbar')||keyboard&&active?.closest('.workspace-toolbar')){show();return}const next=delta>0?1:delta<0?-1:direction;if(next!==direction){direction=next;anchor=previous}if(direction===1&&y-anchor>=16)hidden.value=true;if(direction===-1&&anchor-y>=10)hidden.value=false}
 function keys(e:KeyboardEvent){if(e.key==='Tab')keyboard=true}
 function pointer(){keyboard=false}
 function scroll(){if(!frame)frame=requestAnimationFrame(measure)}
 watch([enabled,page],show);
 onMounted(()=>{motion=window.matchMedia?.('(prefers-reduced-motion: reduce)');motion?.addEventListener('change',show);window.addEventListener('scroll',scroll,{passive:true});document.addEventListener('focusin',show);document.addEventListener('keydown',keys);document.addEventListener('pointerdown',pointer,{passive:true});show()});
 onBeforeUnmount(()=>{cancelAnimationFrame(frame);window.removeEventListener('scroll',scroll);document.removeEventListener('focusin',show);document.removeEventListener('keydown',keys);document.removeEventListener('pointerdown',pointer);motion?.removeEventListener('change',show)});
 return hidden;
}
