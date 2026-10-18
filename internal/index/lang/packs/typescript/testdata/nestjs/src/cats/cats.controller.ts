import { Controller, Delete, Param } from "@nestjs/common";

@Controller({ path: "cats", version: "1" })
export class CatsController {
  @Delete(":id")
  remove(@Param("id") id: string) {}
}
