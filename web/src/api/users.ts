import { enc, http } from './client';
import type { CreateUserRequest, ListResponse, PatchUserRequest, UserView } from './types';

// 以下均仅 admin。

export async function listUsers(): Promise<UserView[]> {
  const res = await http.get<ListResponse<UserView>>('/api/v1/users');
  return res.items ?? [];
}

export function createUser(req: CreateUserRequest): Promise<UserView> {
  return http.post<UserView>('/api/v1/users', req);
}

export function patchUser(name: string, req: PatchUserRequest): Promise<UserView> {
  return http.patch<UserView>(`/api/v1/users/${enc(name)}`, req);
}

export function setUserPassword(name: string, password: string): Promise<void> {
  return http.post<void>(`/api/v1/users/${enc(name)}/password`, { password });
}
