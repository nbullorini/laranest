<script setup lang="ts">
import { computed } from 'vue'
import {
  NConfigProvider, NDialogProvider, NGlobalStyle, NMessageProvider, darkTheme, useOsTheme,
  type GlobalThemeOverrides,
} from 'naive-ui'
import Panel from './Panel.vue'

const os = useOsTheme()
const theme = computed(() => (os.value === 'dark' ? darkTheme : null))

// The logo's red. As text (links, tabs, focus) it needs to stand out from the
// background: bright on dark (5:1), deeper on white. Filled buttons always use
// the deep red, so their white text reads well (5.5:1; the bright red gave 3.5:1).
const accent = { dark: '#f05340', light: '#c62d22' }
const button = { color: '#c62d22', hover: '#d0322a', pressed: '#b8281d' }
const overrides = computed<GlobalThemeOverrides>(() => {
  const a = os.value === 'dark' ? accent.dark : accent.light
  return {
    common: { primaryColor: a, primaryColorHover: a, primaryColorPressed: button.pressed, primaryColorSuppl: a },
    Button: {
      colorPrimary: button.color, colorHoverPrimary: button.hover, colorPressedPrimary: button.pressed, colorFocusPrimary: button.hover,
      borderPrimary: `1px solid ${button.color}`, borderHoverPrimary: `1px solid ${button.hover}`,
      borderPressedPrimary: `1px solid ${button.pressed}`, borderFocusPrimary: `1px solid ${button.hover}`,
      textColorPrimary: '#fff', textColorHoverPrimary: '#fff', textColorPressedPrimary: '#fff', textColorFocusPrimary: '#fff',
      // Disabled ones too (Naive dims the whole button), instead of dark text on red.
      colorDisabledPrimary: button.color, borderDisabledPrimary: `1px solid ${button.color}`, textColorDisabledPrimary: '#fff',
    },
  }
})
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
