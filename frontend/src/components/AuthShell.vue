<script setup lang="ts">
import BrandLogo from './BrandLogo.vue'
</script>
<template>
  <main class="auth-shell">
    <aside class="auth-brand">
      <div class="brand-lockup auth-reveal auth-reveal-1">
        <BrandLogo />
      </div>
      <div class="auth-reveal auth-reveal-2">
        <p class="eyebrow">AI ACCESS, GOVERNED</p>
        <h1>让模型能力<br>在清晰的边界内流动。</h1>
        <p>安全、可控、可审计的 AI API 中转站。</p>
      </div>
      <ul class="auth-reveal auth-reveal-3">
        <li>模型白名单</li>
        <li>双级审批</li>
        <li>标准协议网关</li>
      </ul>
    </aside>
    <section class="auth-form-area">
      <nav class="auth-tools" aria-label="配置文档">
        <a class="auth-tool" href="/codex-guide.html">codex及claudecode配置</a>
        <a class="auth-tool" href="/cc-switch-guide.html">cc-switch配置</a>
      </nav>
      <div class="mobile-brand">
        <BrandLogo />
      </div>
      <div class="auth-form-reveal"><slot /></div>
    </section>
  </main>
</template>
