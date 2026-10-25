import { enc, http } from './client';
import type { BookingView, BookingsResponse, CreateBookingRequest } from './types';

// 窗口内全部预约与可预约节点；所有登录用户可见。vcluster 可选，只取该集群的预约。
export async function listBookings(from: Date, to: Date, vcluster?: string): Promise<BookingsResponse> {
  const q = new URLSearchParams({ from: from.toISOString(), to: to.toISOString() });
  if (vcluster) q.set('vcluster', vcluster);
  const res = await http.get<BookingsResponse>(`/api/v1/bookings?${q.toString()}`);
  return { nodes: res.nodes ?? [], bookings: res.bookings ?? [], now: res.now };
}

export function createBooking(req: CreateBookingRequest): Promise<BookingView> {
  return http.post<BookingView>('/api/v1/bookings', req);
}

export function deleteBooking(name: string): Promise<void> {
  return http.del<void>(`/api/v1/bookings/${enc(name)}`);
}
