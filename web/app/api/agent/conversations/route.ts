import { listHandoffQueue } from "@/lib/handoff-server";

export const dynamic = "force-dynamic";

export async function GET() {
  return listHandoffQueue();
}
