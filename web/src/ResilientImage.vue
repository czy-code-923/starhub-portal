<script setup lang="ts">
import {onBeforeUnmount,ref,watch} from 'vue';
const props=withDefaults(defineProps<{src:string,alt?:string}>(),{alt:''});
const emit=defineEmits<{status:[value:'idle'|'loading'|'retrying'|'ready'|'failed']}>();
type Entry={id:number,src:string,ready:boolean};
const entries=ref<Entry[]>([]);let serial=0,pending=0,attempt=0,timeout:ReturnType<typeof setTimeout>|undefined,retry:ReturnType<typeof setTimeout>|undefined;
function clear(){clearTimeout(timeout);clearTimeout(retry)}
function failed(id:number){if(id!==pending)return;clear();pending=0;entries.value=entries.value.filter(e=>e.id!==id);if(attempt<3){emit('status','retrying');retry=setTimeout(load,attempt===1?1500:4500)}else emit('status','failed')}
function loaded(id:number){if(id!==pending)return;clear();pending=0;entries.value=entries.value.filter(e=>e.id===id).map(e=>({...e,ready:true}));emit('status','ready')}
function load(){if(!props.src)return;attempt++;pending=++serial;const id=pending;entries.value=[...entries.value.filter(e=>e.ready),{id,src:props.src,ready:false}];emit('status',attempt===1?'loading':'retrying');timeout=setTimeout(()=>failed(id),15000)}
watch(()=>props.src,src=>{clear();pending=0;attempt=0;entries.value=entries.value.filter(e=>e.ready);if(src)load();else{entries.value=[];emit('status','idle')}},{immediate:true});
onBeforeUnmount(()=>{pending=0;clear()});
</script>
<template><div class="resilient-image"><img v-for="e in entries" :key="e.id" :src="e.src" :alt="e.ready?alt:''" :class="{ready:e.ready}" :aria-hidden="!e.ready" referrerpolicy="no-referrer" @load="loaded(e.id)" @error="failed(e.id)"></div></template>
<style scoped>.resilient-image{position:relative;overflow:hidden}.resilient-image img{position:absolute;inset:0;width:100%;height:100%;object-fit:cover;opacity:0}.resilient-image img.ready{opacity:1}</style>
