<script setup lang="ts">
import ApiStatus from './components/ApiStatus.vue'
</script>

<template>
  <main class="app">
    <header class="app__header">
      <h1>Smart Campaign Composer</h1>
      <ApiStatus />
    </header>
    <p>Describe a campaign, review the AI proposal, and approve it before anything is sent.</p>
  </main>
</template>

<style>
body {
  margin: 0;
  font-family: system-ui, sans-serif;
  color: #1f2933;
  background: #f8fafc;
}
.app {
  max-width: 960px;
  margin: 0 auto;
  padding: 2rem 1rem;
}
.app__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
}
</style>
