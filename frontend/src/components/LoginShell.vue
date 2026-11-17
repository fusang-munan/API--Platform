<script setup lang="ts">
import { ArrowUpRight } from 'lucide-vue-next'
import LoginPlanet from './LoginPlanet.vue'
</script>

<template>
  <main class="night-login">
    <div class="star-field" aria-hidden="true" />
    <header class="night-header">
      <a class="night-wordmark" href="/login" aria-label="Nebula 首页"><img src="/assets/brand/nebula-n-128.png" alt="" width="48" height="48" /><span>nebula</span></a>
      <nav aria-label="配置文档">
        <a href="/codex-guide.html">Codex / Claude Code <ArrowUpRight :size="14" /></a>
        <a href="/cc-switch-guide.html">CC Switch 配置 <ArrowUpRight :size="14" /></a>
      </nav>
    </header>
    <div class="night-content">
      <section class="night-story" aria-labelledby="night-title">
        <div class="orbital-art" aria-hidden="true"><LoginPlanet /></div>
        <div class="story-copy">
          <h1 id="night-title">每一个灵感，<br>都有<span>无限可能。</span></h1>
          <p class="night-intro">在 Nebula，连接模型与创造力。<br>让强大的 AI 能力，在清晰的边界内自由流动。</p>
        </div>
      </section>
      <section class="night-form" aria-label="账号登录"><slot /></section>
    </div>
  </main>
</template>

<style scoped>
.night-login{--ink:#f5eff9;--ink-soft:#b8adc9;--primary:#cfb8d8;--primary-strong:#f5e3ff;position:relative;isolation:isolate;min-height:100svh;overflow:hidden;display:flex;flex-direction:column;background:radial-gradient(ellipse at 26% 44%,#47406c70,transparent 45%),radial-gradient(ellipse at 80% 78%,#5b518433,transparent 44%),#0e0f1d;color:#f5eff9;padding:0 clamp(24px,5.5vw,100px);font-family:inherit}
.star-field{position:absolute;inset:0;z-index:-1;opacity:.5;pointer-events:none;background-image:radial-gradient(1px 1px at 23px 32px,#cfb8d8 95%,transparent),radial-gradient(1px 1px at 103px 143px,#937497 95%,transparent),radial-gradient(1.5px 1.5px at 180px 80px,#cfb8d8 95%,transparent);background-size:237px 229px,311px 293px,419px 397px;mask-image:linear-gradient(#000,transparent 95%)}
.night-header{display:flex;align-items:center;justify-content:space-between;gap:24px;padding:32px 0;position:relative;z-index:2;border-bottom:1px solid #cfb8d814}
.night-wordmark{display:flex;align-items:center;gap:12px;color:#f5eff9;font-size:32px;font-weight:700;letter-spacing:-1.5px}.night-wordmark img{width:48px;height:48px;object-fit:contain}.night-header nav{display:flex;gap:26px}.night-header nav a{display:flex;align-items:center;gap:6px;font-size:12px;color:#b8adc9}.night-header a:hover{color:#fff}
.night-content{width:100%;max-width:1360px;margin:auto;display:grid;grid-template-columns:1.3fr 1fr;align-items:center;gap:clamp(36px,6vw,110px);padding:58px 0 64px;flex:1}.night-story{position:relative;min-height:570px;display:flex;align-items:flex-end}.story-copy{position:relative;z-index:1}.night-story h1{font-size:clamp(40px,4.3vw,66px);font-weight:550;line-height:1.23;letter-spacing:-2px;margin:0 0 22px}.night-story h1 span{background:linear-gradient(110deg,#f0dff7,#cfb8d8 40%,#937497);background-clip:text;color:transparent}.night-intro{font-size:14px;line-height:1.95;color:#b8adc9;margin:0}
.orbital-art{position:absolute;width:590px;height:410px;top:-45px;left:-30px;pointer-events:none}
.night-form{position:relative;width:100%;max-width:450px;justify-self:end}.night-form::before{content:'';position:absolute;inset:-60px;background:radial-gradient(ellipse,#9374971c,transparent 70%);pointer-events:none;z-index:-1}.night-form :deep(.auth-card){width:100%;max-width:none;margin:0;padding:40px;border-radius:24px;border:1px solid #cfb8d829;background:linear-gradient(140deg,#302b42b8,#171724eb 65%);box-shadow:0 32px 90px #0005,inset 0 1px #cfb8d817;backdrop-filter:blur(20px);color:#f5eff9}.night-form :deep(h1){font-size:27px;font-weight:550;letter-spacing:-.8px;color:#f5eff9;margin:0 0 10px}.night-form :deep(.login-description){font-size:12px;color:#b8adc9;margin:0 0 32px;line-height:1.7}.night-form :deep(form){margin:0}.night-form :deep(fieldset){display:grid;gap:22px;margin:0 0 24px;padding:0;border:0;min-width:0}.night-form :deep(label){display:grid;gap:10px;color:#d6cbdf;font-size:12px;font-weight:500}.night-form :deep(input){width:100%;min-width:0;height:49px;border:1px solid #cfb8d826;border-radius:10px;background:#0e0f1d85;color:#f5eff9;padding:0 14px;box-shadow:inset 0 1px 4px #0002;font-size:13px}.night-form :deep(input::placeholder){color:#93879f}.night-form :deep(input:focus){outline:2px solid #cfb8d899;outline-offset:2px;border-color:#cfb8d8}.night-form :deep(.row){position:relative;display:flex;gap:0}.night-form :deep(.row input){padding-right:46px}.night-form :deep(.icon-button){position:absolute;right:5px;top:5px;width:39px;height:39px;min-height:0;padding:0;border:0;background:transparent;box-shadow:none;color:#b8adc9;display:grid;place-items:center}.night-form :deep(.button.primary){display:flex;align-items:center;justify-content:center;gap:12px;width:100%;height:49px;border:1px solid #e8d3ef70;border-radius:10px;background:linear-gradient(110deg,#cfb8d8,#bca4cc);color:#221b30;font-size:13px;font-weight:700;box-shadow:0 6px 26px #93749733;text-shadow:none}.night-form :deep(.button.primary:hover){background:linear-gradient(110deg,#e1cce9,#cfb8d8);box-shadow:0 8px 30px #93749755}.night-form :deep(.actions){display:flex;justify-content:space-between;gap:10px;flex-wrap:wrap;margin-top:27px;padding-top:23px;border-top:1px solid #cfb8d81a}.night-form :deep(.actions a){font-size:11px;color:#bfb0cf}.night-form :deep(a:hover){color:#f5e3ff}.night-form :deep(.error){color:#ffc3d0;background:#692f4629;padding:10px;border-radius:8px;font-size:12px}.night-form :deep(.identity-user){background:#cfb8d80a;border-color:#cfb8d824}.night-form :deep(.identity-option){background:#cfb8d80a;border-color:#cfb8d824}.night-form :deep(.identity-option:hover){background:#cfb8d814;border-color:#cfb8d880}.night-form :deep(.identity-avatar){background:linear-gradient(135deg,#937497,#47406c)}.night-form :deep(.eyebrow){color:#cfb8d8}.night-login :deep(a:focus-visible),.night-login :deep(button:focus-visible){outline:2px solid #cfb8d8;outline-offset:5px}

@media(min-width:1500px){.night-story{min-height:650px}.orbital-art{top:-5px;transform:scale(1.1);transform-origin:top left}}
@media(max-width:1000px){.night-content{gap:28px;grid-template-columns:1fr 1fr}.night-story h1{font-size:42px}.orbital-art{left:-100px;transform:scale(.85);transform-origin:top center}.night-form :deep(.auth-card){padding:30px}}
@media(max-width:720px){.night-login{padding:0 24px}.night-header{padding:22px 0;gap:12px;align-items:flex-start}.night-wordmark{font-size:26px;gap:7px}.night-wordmark img{width:39px;height:39px}.night-header nav{flex-direction:column;gap:9px;align-items:flex-end}.night-header nav a{font-size:10px}.night-content{grid-template-columns:1fr;gap:32px;padding:36px 0}.night-story{min-height:230px;align-items:center}.night-story h1{font-size:36px;letter-spacing:-1px;margin-bottom:14px}.night-intro{font-size:11px}.orbital-art{left:auto;right:-235px;top:-95px;transform:scale(.7);opacity:.7}.night-form{max-width:460px;justify-self:center}.night-form :deep(.auth-card){padding:30px 26px;border-radius:20px}.night-form :deep(h1){font-size:25px}.night-form :deep(.login-description){margin-bottom:26px}}
@media(prefers-reduced-motion:no-preference){.story-copy,.night-form{animation:night-enter .8s ease-out both}.night-form{animation-delay:.12s}@keyframes night-enter{from{opacity:0;transform:translateY(12px)}to{opacity:1;transform:none}}}
</style>
