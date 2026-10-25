import { useTheme, type ThemePreference } from '../theme/ThemeContext';

const OPTIONS: { value: ThemePreference; label: string }[] = [
  { value: 'system', label: '跟随系统' },
  { value: 'light', label: '浅色' },
  { value: 'dark', label: '深色' },
];

// 外观三选一（radio），直接写 ThemeContext。
export function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  return (
    <fieldset className="theme-toggle">
      <legend>外观</legend>
      {OPTIONS.map((o) => (
        <label key={o.value} className="theme-option">
          <input type="radio" name="theme" value={o.value} checked={theme === o.value} onChange={() => setTheme(o.value)} />
          <span>{o.label}</span>
        </label>
      ))}
    </fieldset>
  );
}
