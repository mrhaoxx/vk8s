import { ButtonLink } from '../components/Button';
import { EmptyState } from '../components/State';

export function NotFoundPage() {
  return <EmptyState title="页面不存在" action={<ButtonLink to="/">返回我的集群</ButtonLink>} />;
}
