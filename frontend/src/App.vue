<script setup lang="ts">
import { RouterLink, RouterView } from 'vue-router'
import ApiStatus from './components/ApiStatus.vue'
</script>

<template>
  <div class="app">
    <header class="app__header">
      <h1>Smart Campaign Composer</h1>
      <nav aria-label="Main">
        <RouterLink to="/campaigns">
          Campaigns
        </RouterLink>
        <RouterLink to="/campaigns/new">
          New campaign
        </RouterLink>
      </nav>
      <ApiStatus />
    </header>
    <main>
      <RouterView />
    </main>
  </div>
</template>

<style>
body {
  margin: 0;
  font-family: system-ui, sans-serif;
  color: #1f2933;
  background: #f8fafc;
}
.app {
  max-width: 1100px;
  margin: 0 auto;
  padding: 1.5rem 1rem 3rem;
}
.app__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 1.5rem;
}
.app__header h1 {
  margin: 0;
  font-size: 1.5rem;
}
.app__header nav {
  display: flex;
  gap: 1rem;
}
a {
  color: #1d4ed8;
}
a.router-link-exact-active {
  font-weight: 600;
}
button {
  font: inherit;
  padding: 0.45rem 0.9rem;
  border-radius: 6px;
  border: 1px solid #1d4ed8;
  background: #1d4ed8;
  color: #fff;
  cursor: pointer;
}
button.secondary {
  background: #fff;
  color: #1d4ed8;
}
button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
button:focus-visible,
a:focus-visible,
input:focus-visible,
textarea:focus-visible {
  outline: 3px solid #93c5fd;
  outline-offset: 2px;
}
.panel {
  background: #fff;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  padding: 1rem 1.25rem;
  margin-bottom: 1rem;
}
.panel h2,
.panel h3 {
  margin-top: 0;
}
.error {
  color: #991b1b;
  background: #fee2e2;
  border-radius: 6px;
  padding: 0.5rem 0.75rem;
}
.warning {
  color: #78350f;
  background: #fef3c7;
  border-radius: 6px;
  padding: 0.5rem 0.75rem;
}
.muted {
  color: #64748b;
  font-size: 0.875rem;
}
</style>
