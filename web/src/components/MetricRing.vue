<script setup lang="ts">
import { computed } from 'vue'
import { usageColor } from '@/utils/status'

const props = withDefaults(
  defineProps<{
    /** 环形填充比例；null 表示暂无数据 */
    percentage: number | null
    /** 圆心主文本，默认显示四舍五入后的百分比 */
    display?: string
    /** 圆心主文本后缀，默认 %；传空串可隐藏 */
    unit?: string
    label?: string
    caption?: string
    size?: number
    stroke?: number
    /** 覆盖默认水位配色；用于在线率这类「越高越好」的指标 */
    color?: string
  }>(),
  { display: '', unit: '%', label: '', caption: '', size: 112, stroke: 10, color: '' },
)

const pct = computed(() => (props.percentage == null ? 0 : Math.max(0, Math.min(100, Math.round(props.percentage)))))
const color = computed(() => props.color || usageColor(props.percentage))
const radius = computed(() => (props.size - props.stroke) / 2)
const circumference = computed(() => 2 * Math.PI * radius.value)
const dash = computed(() => (pct.value / 100) * circumference.value)
const centerText = computed(() =>
  props.display || (props.percentage == null ? '—' : String(Math.round(props.percentage))),
)
</script>

<template>
  <div class="metric-ring">
    <div class="ring-canvas" :style="{ width: `${size}px`, height: `${size}px` }">
      <svg :viewBox="`0 0 ${size} ${size}`" width="100%" height="100%">
        <circle class="ring-track" :cx="size / 2" :cy="size / 2" :r="radius" :stroke-width="stroke" fill="none" />
        <circle
          class="ring-value"
          :cx="size / 2"
          :cy="size / 2"
          :r="radius"
          :stroke-width="stroke"
          fill="none"
          stroke-linecap="round"
          :stroke="color"
          :stroke-dasharray="`${dash} ${circumference - dash}`"
          :transform="`rotate(-90 ${size / 2} ${size / 2})`"
        />
      </svg>
      <div class="ring-center">
        <span class="ring-num" :style="{ color }">
          {{ centerText }}<em v-if="unit && percentage != null">{{ unit }}</em>
        </span>
      </div>
    </div>
    <div v-if="label" class="ring-label">{{ label }}</div>
    <div v-if="caption" class="ring-caption">{{ caption }}</div>
  </div>
</template>

<style scoped>
.metric-ring {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
}

.ring-canvas {
  position: relative;
}

.ring-track {
  stroke: #edf0f7;
}

.ring-value {
  transition: stroke-dasharray 0.5s ease, stroke 0.3s ease;
}

.ring-center {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
}

.ring-num {
  font-size: 20px;
  font-weight: 650;
  letter-spacing: 0.2px;
  line-height: 1;
}

.ring-num em {
  font-style: normal;
  font-size: 11px;
  font-weight: 500;
  margin-left: 1px;
}

.ring-label {
  margin-top: 6px;
  font-size: 12px;
  color: var(--pr-text);
}

.ring-caption {
  font-size: 11px;
  color: var(--pr-sub);
  text-align: center;
  white-space: nowrap;
}
</style>
