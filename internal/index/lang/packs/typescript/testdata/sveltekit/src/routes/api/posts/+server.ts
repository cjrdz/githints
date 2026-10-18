import { json } from "@sveltejs/kit";

export async function GET() {
  return json([]);
}

export const POST = async () => json({}, { status: 201 });
