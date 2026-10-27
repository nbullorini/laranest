<script setup lang="ts">
import { computed } from 'vue'
import {
  NConfigProvider, NDialogProvider, NGlobalStyle, NMessageProvider, darkTheme, useOsTheme,
  type GlobalThemeOverrides,
} from 'naive-ui'
import Panel from './Panel.vue'

const os = useOsTheme()
const theme = computed(() => (os.value === 'dark' ? darkTheme : null))

const brand = '#f05340'
const overrides: GlobalThemeOverrides = {
  common: { primaryColor: brand, primaryColorHover: '#f36b5a', primaryColorPressed: '#d94531', primaryColorSuppl: brand },
  // Dark theme defaults to black text on primary buttons; white reads better on the brand red.
  Button: { textColorPrimary: '#fff', textColorHoverPrimary: '#fff', textColorPressedPrimary: '#fff', textColorFocusPrimary: '#fff' },
}
</script>

<template>
  <!-- Providers must wrap the component that calls useMessage/useDialog. -->
  <NConfigProvider :theme="theme" :theme-overrides="overrides">
    <NGlobalStyle />
    <NMessageProvider :duration="5000" closable>
      <NDialogProvider>
        <Panel />
      </NDialogProvider>
    </NMessageProvider>
  </NConfigProvider>
</template>
