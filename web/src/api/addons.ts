import { enc, http } from './client';
import type { AddonInstallView, AddonOutputValue, AddonRequest, AddonTemplateView } from './types';

export async function listAddonCatalog(): Promise<AddonTemplateView[]> {
  return (await http.get<AddonTemplateView[]>('/api/v1/addons')) ?? [];
}

export async function listClusterAddons(vc: string): Promise<AddonInstallView[]> {
  const items = (await http.get<AddonInstallView[]>(`/api/v1/vclusters/${enc(vc)}/addons`)) ?? [];
  return items.map((i) => ({ ...i, inputs: i.inputs ?? {}, outputs: i.outputs ?? [] }));
}

export function installAddon(vc: string, req: AddonRequest): Promise<AddonInstallView> {
  return http.post<AddonInstallView>(`/api/v1/vclusters/${enc(vc)}/addons`, req);
}

export function uninstallAddon(vc: string, template: string): Promise<void> {
  return http.del<void>(`/api/v1/vclusters/${enc(vc)}/addons/${enc(template)}`);
}

export function upgradeAddon(vc: string, template: string): Promise<void> {
  return http.post<void>(`/api/v1/vclusters/${enc(vc)}/addons/${enc(template)}/upgrade`);
}

export function retryAddon(vc: string, template: string): Promise<void> {
  return http.post<void>(`/api/v1/vclusters/${enc(vc)}/addons/${enc(template)}/retry`);
}

export async function getAddonOutputs(vc: string, template: string): Promise<AddonOutputValue[]> {
  return (await http.get<AddonOutputValue[]>(`/api/v1/vclusters/${enc(vc)}/addons/${enc(template)}/outputs`)) ?? [];
}

export function getAddonLogs(vc: string, template: string): Promise<{ log: string }> {
  return http.get<{ log: string }>(`/api/v1/vclusters/${enc(vc)}/addons/${enc(template)}/logs`);
}
