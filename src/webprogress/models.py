from pydantic import BaseModel


class ClientPayload(BaseModel):
    progress: float
    total: float
    description: str
    key: str
