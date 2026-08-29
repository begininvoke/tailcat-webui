import { App as AntApp, ConfigProvider, theme } from 'antd'
import enUS from 'antd/locale/en_US'
import zhCN from 'antd/locale/zh_CN'
import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import i18n, { type Language } from '../i18n'

export type ThemeChoice = 'light' | 'dark' | 'system'

interface PreferencesValue {
  themeChoice: ThemeChoice
  resolvedTheme: 'light' | 'dark'
  setThemeChoice: (choice: ThemeChoice) => void
  language: Language
  setLanguage: (language: Language) => void
}

const PreferencesContext = createContext<PreferencesValue | null>(null)

function initialTheme(): ThemeChoice {
  const value = localStorage.getItem('tailcat-theme')
  return value === 'light' || value === 'dark' || value === 'system' ? value : 'system'
}

function initialLanguage(): Language {
  const value = localStorage.getItem('tailcat-language')
  if (value === 'en' || value === 'zh-CN') return value
  return navigator.language.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en'
}

export function PreferencesProvider({ children }: { children: ReactNode }) {
  const [themeChoice, setThemeChoiceState] = useState<ThemeChoice>(initialTheme)
  const [systemDark, setSystemDark] = useState(() => matchMedia('(prefers-color-scheme: dark)').matches)
  const [language, setLanguageState] = useState<Language>(initialLanguage)
  const resolvedTheme = themeChoice === 'system' ? (systemDark ? 'dark' : 'light') : themeChoice

  useEffect(() => {
    const media = matchMedia('(prefers-color-scheme: dark)')
    const listener = (event: MediaQueryListEvent) => setSystemDark(event.matches)
    media.addEventListener('change', listener)
    return () => media.removeEventListener('change', listener)
  }, [])

  useEffect(() => {
    document.documentElement.dataset.theme = resolvedTheme
  }, [resolvedTheme])

  useEffect(() => {
    void i18n.changeLanguage(language)
    document.documentElement.lang = language
  }, [language])

  const value = useMemo<PreferencesValue>(() => ({
    themeChoice,
    resolvedTheme,
    setThemeChoice: (choice) => {
      localStorage.setItem('tailcat-theme', choice)
      setThemeChoiceState(choice)
    },
    language,
    setLanguage: (next) => {
      localStorage.setItem('tailcat-language', next)
      setLanguageState(next)
    },
  }), [language, resolvedTheme, themeChoice])

  return (
    <PreferencesContext.Provider value={value}>
      <ConfigProvider
        locale={language === 'zh-CN' ? zhCN : enUS}
        theme={{
          algorithm: resolvedTheme === 'dark' ? theme.darkAlgorithm : theme.defaultAlgorithm,
          token: {
            colorPrimary: resolvedTheme === 'dark' ? '#42c4cf' : '#00656f',
            colorBgBase: resolvedTheme === 'dark' ? '#0e1213' : '#f4f6f5',
            colorTextBase: resolvedTheme === 'dark' ? '#eef4f3' : '#152022',
            colorTextSecondary: resolvedTheme === 'dark' ? '#a9b6b8' : '#556467',
            colorBorder: resolvedTheme === 'dark' ? '#30383a' : '#d9e0de',
            colorLink: resolvedTheme === 'dark' ? '#70d6de' : '#08727e',
            colorLinkHover: resolvedTheme === 'dark' ? '#9ae5eb' : '#075d66',
            colorSuccess: resolvedTheme === 'dark' ? '#58c995' : '#298b62',
            colorWarning: resolvedTheme === 'dark' ? '#e3a14b' : '#b66a16',
            colorError: resolvedTheme === 'dark' ? '#ff8f8f' : '#c23b3b',
            controlItemBgActive: resolvedTheme === 'dark' ? '#153638' : '#dfeceb',
            controlItemBgActiveHover: resolvedTheme === 'dark' ? '#1d474a' : '#d2e4e2',
            borderRadius: 8,
            borderRadiusLG: 12,
            controlHeight: 44,
            fontFamily: '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif',
          },
          components: {
            Layout: { bodyBg: resolvedTheme === 'dark' ? '#0e1213' : '#f4f6f5', siderBg: resolvedTheme === 'dark' ? '#121719' : '#fbfcfc', headerBg: 'transparent' },
            Button: {
              primaryColor: resolvedTheme === 'dark' ? '#000000' : '#ffffff',
              dangerColor: resolvedTheme === 'dark' ? '#000000' : '#ffffff',
            },
            Card: { boxShadow: 'none' },
            Menu: {
              itemBorderRadius: 8,
              itemHeight: 44,
              itemSelectedBg: resolvedTheme === 'dark' ? '#153638' : '#dfeceb',
              itemSelectedColor: resolvedTheme === 'dark' ? '#8be1e7' : '#00555e',
            },
            Radio: { buttonSolidCheckedColor: resolvedTheme === 'dark' ? '#000000' : '#ffffff' },
            Drawer: { footerPaddingBlock: 16, footerPaddingInline: 20 },
          },
        }}
      >
        <AntApp>{children}</AntApp>
      </ConfigProvider>
    </PreferencesContext.Provider>
  )
}

export function usePreferences() {
  const value = useContext(PreferencesContext)
  if (!value) throw new Error('usePreferences must be used inside PreferencesProvider')
  return value
}
