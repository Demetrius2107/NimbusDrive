// 统一主题配置：渐变彩色风格。
// 主色蓝→紫渐变，侧栏深色靛紫，按钮渐变填充，配额环形渐变。
import type { ThemeConfig } from 'antd'

// 渐变色谱常量，供内联 style 和 CSS 共用。
export const palette = {
  // 主色系
  primary: '#3b82f6', // blue-500
  primaryDark: '#6366f1', // indigo-500
  accent: '#8b5cf6', // violet-500
  accentLight: '#a78bfa', // violet-400

  // 侧栏深色渐变
  siderFrom: '#1e1b4b', // indigo-950
  siderTo: '#312e81', // indigo-900

  // 背景与表面
  bgLayout: '#f5f6fb',
  bgGlass: 'rgba(255, 255, 255, 0.72)',

  // 渐变定义
  gradientPrimary: 'linear-gradient(135deg, #3b82f6 0%, #8b5cf6 100%)',
  gradientPrimaryHover: 'linear-gradient(135deg, #2563eb 0%, #7c3aed 100%)',
  gradientSider: 'linear-gradient(180deg, #1e1b4b 0%, #312e81 100%)',
  gradientLogo: 'linear-gradient(135deg, #60a5fa 0%, #a78bfa 100%)',
  gradientLoginBg: 'linear-gradient(135deg, #1e1b4b 0%, #3b82f6 50%, #8b5cf6 100%)',

  // 文本
  textOnDark: '#e0e7ff',
  textSecondary: '#64748b',
} as const

export const themeConfig: ThemeConfig = {
  token: {
    colorPrimary: palette.primary,
    colorInfo: palette.primary,
    colorLink: palette.primary,
    colorBgLayout: palette.bgLayout,
    borderRadius: 10,
    fontFamily:
      "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'PingFang SC', 'Microsoft YaHei', sans-serif",
  },
  components: {
    Layout: {
      siderBg: 'transparent',
      headerBg: 'transparent',
      headerHeight: 60,
      bodyBg: palette.bgLayout,
    },
    Menu: {
      darkItemBg: 'transparent',
      darkSubMenuItemBg: 'transparent',
      darkItemSelectedBg: 'rgba(139, 92, 246, 0.25)',
      darkItemHoverBg: 'rgba(139, 92, 246, 0.15)',
      darkItemColor: palette.textOnDark,
      darkItemSelectedColor: '#ffffff',
      itemHeight: 44,
      itemMarginInline: 8,
      itemBorderRadius: 8,
    },
    Card: {
      borderRadiusLG: 14,
      boxShadowTertiary: '0 1px 3px rgba(30, 27, 75, 0.06), 0 4px 16px rgba(30, 27, 75, 0.04)',
    },
    Button: {
      primaryShadow: '0 2px 8px rgba(99, 102, 241, 0.3)',
    },
    Table: {
      headerBg: '#faf9ff',
      headerColor: palette.textSecondary,
      rowHoverBg: '#f3f0ff',
      borderColor: '#ede9fe',
    },
  },
}
