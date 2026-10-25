// 色块按集群着色：集群名散列到色相，低饱和以配合纸面配色；文字始终白色。
export function hashHue(name: string): number {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return h % 360;
}

export function clusterColor(name: string, dark: boolean): string {
  const hue = hashHue(name);
  return dark ? `hsl(${hue} 28% 40%)` : `hsl(${hue} 32% 46%)`;
}
