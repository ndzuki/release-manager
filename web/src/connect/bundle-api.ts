import { t } from '@/i18n/messages';
import { Code, ConnectError } from '@connectrpc/connect';
import { create } from '@bufbuild/protobuf';
import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt';
import { BundleStatus } from '@/gen/common/v1/domain_pb';
import { PaginationSchema } from '@/gen/common/v1/types_pb';
import {
  GetBundleRequestSchema,
  ListBundlesRequestSchema,
  type BundleSummary,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { bundleClient } from './client';
import { correlationLine, describeError } from './error-copy';

/*
 * Release bundle browsing (REQ-046/REQ-069 surface, A8 of the UX plan's missing
 * surfaces). The console only used bundles as a hidden dropdown in the operation
 * form; there was no way to see what a bundle contains, its digests or its evidence
 * references.
 *
 * Contract notes (api/proto/orchestrator/v1/orchestrator.proto):
 *  - ListBundles REQUIRES `release_definition_id` for a console caller (it is both
 *    the scope and the authorization key; omitting it answers PERMISSION_DENIED
 *    `not_authorized`), and GetBundle requires it alongside `bundle_id`;
 *  - ListBundles is cursor-paginated with an OPAQUE token bound to the filter that
 *    produced it, "so a filter change must restart from the first page";
 *  - a bundle set the caller may not see answers PERMISSION_DENIED rather than an
 *    empty page — the two must not look alike in the UI;
 *  - page size is clamped server-side to [1, 100] and defaults to 20.
 */

export const BUNDLE_STATUSES: Array<{ value: BundleStatus; label: string }> = [
  { value: BundleStatus.RECEIVED, label: '已接收' },
  { value: BundleStatus.VALIDATED, label: '已校验' },
  { value: BundleStatus.REJECTED, label: '已拒绝' },
  { value: BundleStatus.ARCHIVED, label: '已归档' },
];

export interface BundleImageView {
  ref: string;
  digest: string;
  valuesPath: string;
}

export interface BundleSummaryView {
  id: string;
  name: string;
  digest: string;
  status: BundleStatus;
  statusLabel: string;
  chartRef: string;
  chartVersion: string;
  chartDigest: string;
  images: BundleImageView[];
  createdAt: string | null;
}

export interface BundleDetailView extends BundleSummaryView {
  gitCommit: string;
  pipelineId: string;
  signatureDigest: string;
  sbomDigest: string;
  provenanceDigest: string;
  signatureRef: string;
  sbomRef: string;
  provenanceRef: string;
}

export interface BundleFilters {
  releaseDefinitionId?: string;
  chartNameFilter?: string;
  status?: BundleStatus | null;
}

export interface BundlePage {
  bundles: BundleSummaryView[];
  nextPageToken: string;
  totalSize: number;
}

function toIso(value: Timestamp | undefined): string | null {
  return value ? timestampDate(value).toISOString() : null;
}

export function statusLabel(status: BundleStatus): string {
  return BUNDLE_STATUSES.find((entry) => entry.value === status)?.label ?? '未知';
}

function fromSummary(summary: BundleSummary): BundleSummaryView {
  return {
    id: summary.id,
    name: summary.name,
    digest: summary.digest ? `${summary.digest.algorithm}:${summary.digest.value}` : '',
    status: summary.status,
    statusLabel: statusLabel(summary.status),
    chartRef: summary.chartRef,
    chartVersion: summary.chartVersion,
    chartDigest: summary.chartDigest,
    images: summary.images.map((image) => ({ ref: image.ref, digest: image.digest, valuesPath: image.valuesPath })),
    createdAt: toIso(summary.createdAt),
  };
}

export async function listBundles(
  filters: BundleFilters = {},
  pageToken = '',
  pageSize = 20,
): Promise<BundlePage> {
  const response = await bundleClient.listBundles(
    create(ListBundlesRequestSchema, {
      releaseDefinitionId: filters.releaseDefinitionId ?? '',
      chartNameFilter: filters.chartNameFilter ?? '',
      statusFilter: filters.status ? [filters.status] : [],
      pagination: create(PaginationSchema, { pageSize, pageToken }),
    }),
  );
  return {
    bundles: response.bundles.map(fromSummary),
    nextPageToken: response.pagination?.nextPageToken ?? '',
    totalSize: response.pagination?.totalSize ?? 0,
  };
}

export async function getBundle(bundleId: string, releaseDefinitionId: string): Promise<BundleDetailView> {
  // Both identifiers are mandatory for a console caller: the id selects the bundle,
  // the definition is what the caller is authorized against.
  const response = await bundleClient.getBundle(create(GetBundleRequestSchema, { bundleId, releaseDefinitionId }));
  const detail = response.bundle;
  if (!detail?.summary) {
    // The contract promises a bundle when the call succeeds; a missing one is a
    // contract violation rather than an empty detail view.
    throw new Error(t('bundle.api.missingDetail'));
  }
  return {
    ...fromSummary(detail.summary),
    gitCommit: detail.gitCommit,
    pipelineId: detail.pipelineId,
    signatureDigest: detail.signatureDigest,
    sbomDigest: detail.sbomDigest,
    provenanceDigest: detail.provenanceDigest,
    signatureRef: detail.signatureRef,
    sbomRef: detail.sbomRef,
    provenanceRef: detail.provenanceRef,
  };
}

export type BundleFailureCode = 'permission_denied' | 'not_found' | 'stale_cursor' | 'invalid_input' | 'unavailable';

export interface BundleFailure {
  code: BundleFailureCode;
  message: string;
  retryable: boolean;
  details: string;
}

const MESSAGES: Record<BundleFailureCode, string> = {
  permission_denied: '无权查看这些 Bundle（不是「没有数据」——服务端对不可见集合返回拒绝）',
  not_found: '未找到该 Bundle',
  stale_cursor: '分页游标已失效（游标与过滤条件绑定），已回到第一页，请重试',
  invalid_input: '输入不合法：请检查分页大小与过滤条件（游标已失效时会自动回到第一页）',
  unavailable: '加载失败，请稍后重试',
};

export function mapBundleError(error: unknown): BundleFailure {
  const connectError = ConnectError.from(error);
  const pick = (code: BundleFailureCode, retryable = false): BundleFailure => ({
    code,
    message: MESSAGES[code],
    retryable,
    details: correlationLine(describeError(error)),
  });

  if (connectError.code === Code.PermissionDenied) return pick('permission_denied');
  if (connectError.code === Code.NotFound) return pick('not_found');
  if (connectError.code === Code.InvalidArgument) {
    // The bundle service sets NO reason code, so there is nothing stable to read
    // here. A stale cursor is handled structurally by the store (drop the token and
    // reload page 1); this mapping stays code-only rather than sniffing free text.
    return pick('invalid_input', true);
  }
  return pick('unavailable', true);
}
