<script setup lang="ts">
import {computed,onMounted,onBeforeUnmount,ref} from 'vue';import ResilientImage from './ResilientImage.vue';
const props=defineProps<{desktop:string,mobile:string,dim:number}>();
const narrow=ref(!!window.matchMedia?.('(max-width: 760px)').matches);let query:MediaQueryList|undefined;
function changed(){narrow.value=!!query?.matches}
onMounted(()=>{query=window.matchMedia?.('(max-width: 760px)');changed();query?.addEventListener('change',changed)});
onBeforeUnmount(()=>query?.removeEventListener('change',changed));
const src=computed(()=>narrow.value?(props.mobile||props.desktop):(props.desktop||props.mobile));
</script>
<template><div class="site-background" aria-hidden="true"><ResilientImage :src="src"/><div class="background-shade" :style="{opacity:Math.min(85,Math.max(0,Number(dim)||0))/100}"></div></div></template>
<style scoped>.site-background>.resilient-image,.background-shade{position:absolute;inset:0;width:100%;height:100%}</style>
