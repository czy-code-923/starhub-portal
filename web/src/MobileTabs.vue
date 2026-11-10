<script setup lang="ts">
import {nextTick,onMounted,ref,watch} from 'vue';
const props=defineProps<{tabs:string[][],page:string,disabled:boolean}>();const emit=defineEmits<{navigate:[page:string]}>();const strip=ref<HTMLElement>();
async function reveal(){await nextTick();const active=strip.value?.querySelector<HTMLElement>('[aria-current="page"]');if(!active||!strip.value)return;const box=strip.value,left=active.offsetLeft-(box.clientWidth-active.offsetWidth)/2;box.scrollTo?.({left,behavior:'auto'})}
watch(()=>[props.page,props.tabs],reveal);onMounted(reveal);
</script>
<template><nav ref="strip" class="mobile-tabs" aria-label="横向栏目导航"><button v-for="t in tabs" :key="t[0]" type="button" :aria-current="page===t[0]?'page':undefined" :disabled="disabled" @click="emit('navigate',t[0])"><span aria-hidden="true">{{t[2]}}</span>{{t[1]}}</button></nav></template>
<style scoped>
.mobile-tabs{display:none}@media(max-width:760px){.mobile-tabs{grid-column:1/-1;position:relative;display:flex;gap:4px;overflow-x:auto;overscroll-behavior-x:contain;scrollbar-width:none;min-width:0;padding:4px 0 0;border-top:1px solid color-mix(in srgb,var(--accent) 14%,transparent);scroll-padding-inline:12px}.mobile-tabs::-webkit-scrollbar{display:none}.mobile-tabs button{flex:none;display:flex;gap:5px;align-items:center;min-height:34px;padding:6px 11px;border:0!important;border-radius:9px;background:transparent!important;font-size:12px;white-space:nowrap;color:var(--muted)!important}.mobile-tabs button[aria-current=page]{background:var(--accent)!important;color:var(--surface)!important;font-weight:600}.mobile-tabs button:focus-visible{outline:2px solid var(--accent);outline-offset:-2px}.mobile-tabs button>span{font-size:13px}}
</style>
