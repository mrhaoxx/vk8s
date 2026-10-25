import { enc, http } from './client';
import type { CreateTokenRequest, CreateTokenResponse, ListResponse, TokenView } from './types';

export async function listTokens(vcluster: string): Promise<TokenView[]> {
  const res = await http.get<ListResponse<TokenView>>(`/api/v1/vclusters/${enc(vcluster)}/tokens`);
  return res.items ?? [];
}

// 返回的 kubeconfig 明文只出现这一次。
export function createToken(vcluster: string, req: CreateTokenRequest): Promise<CreateTokenResponse> {
  return http.post<CreateTokenResponse>(`/api/v1/vclusters/${enc(vcluster)}/tokens`, req);
}

export function revokeToken(id: string): Promise<void> {
  return http.del<void>(`/api/v1/tokens/${enc(id)}`);
}
